package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"yunling.local/platform/internal/agent"
	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/agentupdate"
	"yunling.local/platform/internal/executor"
	"yunling.local/platform/internal/logstream"
)

var agentVersion = "0.1.0"

func main() {
	if writeVersionCommand(os.Args, os.Stdout) {
		return
	}
	if handled, err := runApplyUpgradeCommand(os.Args, agentupdate.DefaultRoot, agentupdate.NewSystemController(), agentupdate.Apply); handled {
		if err != nil {
			log.Fatalf("应用代理升级失败：%v", err)
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "run-spec" {
		exitCode, err := executor.RunSystemdSpec(os.Args[2])
		if err != nil {
			log.Printf("执行隔离任务失败：%v", err)
		}
		if exitCode < 0 {
			exitCode = 1
		}
		os.Exit(exitCode)
	}
	credentialsPath := os.Getenv("YUNLING_CREDENTIALS_PATH")
	if credentialsPath == "" {
		credentialsPath = agent.DefaultCredentialsPath
	}
	credentials, err := agent.LoadCredentials(credentialsPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Fatalf("读取云令代理凭据失败：%v", err)
		}
		enrollmentToken := os.Getenv("YUNLING_ENROLLMENT_TOKEN")
		controlURL := os.Getenv("YUNLING_CONTROL_URL")
		if enrollmentToken == "" || controlURL == "" {
			log.Fatal("代理尚未注册，请设置 YUNLING_CONTROL_URL 和一次性 YUNLING_ENROLLMENT_TOKEN")
		}
		credentials, err = agent.Enroll(context.Background(), controlURL, enrollmentToken)
		if err != nil {
			log.Fatalf("注册云令代理失败：%v", err)
		}
		if err := agent.SaveCredentials(credentialsPath, credentials); err != nil {
			log.Fatalf("保存云令代理独立凭据失败：%v", err)
		}
		log.Printf("云令代理注册成功，凭据已保存至 %s", credentialsPath)
	}

	diskPath := os.Getenv("YUNLING_WORK_DIR")
	if diskPath == "" {
		diskPath = "/var/lib/yunling-agent"
		if _, err := os.Stat(diskPath); err != nil {
			diskPath = "."
		}
	}
	runtimeConfig := os.Getenv("YUNLING_RUNTIMES")
	if runtimeConfig == "" {
		runtimeConfig = "bash,python3"
	}
	var runtimes []string
	for _, runtimeName := range strings.Split(runtimeConfig, ",") {
		if runtimeName = strings.TrimSpace(runtimeName); runtimeName != "" {
			runtimes = append(runtimes, runtimeName)
		}
	}
	cacheRoot := filepath.Join(diskPath, "script-cache")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sender, err := agent.DialHeartbeatSender(ctx, credentials.ControlURL, credentials.Credential)
	if err != nil {
		log.Fatalf("连接云令中央服务失败：%v", err)
	}
	defer sender.Close()
	if err := confirmPendingUpgrade(ctx, agentupdate.DefaultRoot, agentVersion, sender, time.Now); err != nil {
		log.Fatalf("确认代理升级重连失败：%v", err)
	}
	spoolMaxBytes := logstream.DefaultSpoolMaxBytes
	if configured := strings.TrimSpace(os.Getenv("YUNLING_LOG_SPOOL_MAX_BYTES")); configured != "" {
		spoolMaxBytes, err = strconv.ParseInt(configured, 10, 64)
		if err != nil || spoolMaxBytes <= 0 {
			log.Fatal("YUNLING_LOG_SPOOL_MAX_BYTES 必须是正整数")
		}
	}
	spool, err := logstream.NewSpool(
		filepath.Join(diskPath, "log-spool"), logstream.DefaultChunkSize,
		logstream.WithSpoolMaxBytes(spoolMaxBytes),
	)
	if err != nil {
		log.Fatalf("初始化本地日志缓冲失败：%v", err)
	}
	logClient := agent.NewLogClient(spool, sender)
	runner := executor.NewRunner(
		newAgentLauncher(runtime.GOOS, os.Getenv("YUNLING_EXECUTION_MODE")),
		10*time.Second,
		executor.WithWorkRoot(filepath.Join(diskPath, "runs")),
		executor.WithAllowedScriptRoots(filepath.Join(cacheRoot, "scripts")),
		executor.WithAllowedRuntimes(runtimes...),
		executor.WithOutputSink(logClient),
	)
	stats, err := agent.NewSystemStatsSource(diskPath, runner.RunningCount)
	if err != nil {
		log.Fatalf("初始化系统资源采集失败：%v", err)
	}
	collector := agent.NewCollector(
		stats,
		runtimes,
		agent.WithLogSpool(spool),
		agent.WithUpgradeState(func() (*agentprotocol.UpgradeRuntimeState, error) {
			return agentupdate.LoadRuntimeState(agentupdate.DefaultRoot)
		}),
	)
	allowedRoots := agent.ParseAllowedScriptRoots(os.Getenv("YUNLING_ALLOWED_SCRIPT_ROOTS"))
	if len(allowedRoots) > 0 {
		discovered, err := executor.NewDiscovery().List(context.Background(), allowedRoots)
		if err != nil {
			log.Fatalf("扫描允许脚本目录失败：%v", err)
		}
		log.Printf("已从允许目录发现 %d 个可导入脚本", len(discovered))
	}

	heartbeatSequenceFloor := time.Now().UTC().UnixMilli()
	if heartbeatSequenceFloor < 0 {
		heartbeatSequenceFloor = 0
	}
	heartbeatClient := agent.NewClient(
		credentials.ServerID,
		agentVersion,
		collector,
		sender,
		agent.WithPlatform(runtime.GOOS, runtime.GOARCH, detectedCapabilities(runtime.GOOS, os.Stat)),
		agent.WithInitialHeartbeatSequence(uint64(heartbeatSequenceFloor)),
	)
	cache := executor.NewCache(cacheRoot, agent.NewCredentialDownloader(credentials.Credential, nil))
	syncClient := agent.NewSyncClient(cache, executor.NewDriftScanner(cacheRoot), sender)
	executionClient := agent.NewExecutionClient(runner, sender)
	upgradeManager := agentupdate.NewManager(
		agentupdate.DefaultRoot,
		agentupdate.HTTPDownloader{},
		agentupdate.NewSystemdStarter(),
		nil,
	)
	upgradeClient := agent.NewUpgradeClient(upgradeManager, sender, time.Now)
	if err := sender.SendRunningReport(ctx, agentprotocol.RunningReport{
		ServerID: credentials.ServerID, ReportedAt: time.Now().UTC(), Authoritative: false, Processes: runner.RunningProcesses(),
	}); err != nil {
		log.Fatalf("上报代理重连状态失败：%v", err)
	}
	log.Printf("云令代理已连接，服务器编号：%s", credentials.ServerID)
	errors := make(chan error, 5)
	go func() { errors <- heartbeatClient.Run(ctx) }()
	go func() { errors <- syncClient.Run(ctx) }()
	go func() { errors <- executionClient.Run(ctx) }()
	go func() { errors <- logClient.Run(ctx) }()
	go func() { errors <- upgradeClient.Run(ctx) }()
	select {
	case <-ctx.Done():
		return
	case err := <-errors:
		if err != nil {
			log.Fatalf("云令代理停止：%v", err)
		}
	}
}

type upgradeEventSender interface {
	SendUpgradeEvent(context.Context, agentprotocol.UpgradeEvent) error
}

func confirmPendingUpgrade(ctx context.Context, root, version string, sender upgradeEventSender, now func() time.Time) error {
	state, err := agentupdate.LoadRuntimeState(root)
	if err != nil || state == nil {
		return err
	}
	if state.TargetVersion != version {
		return nil
	}
	switch state.Stage {
	case agentprotocol.StageInstalling, agentprotocol.StageRollingBack, agentprotocol.StageReconnecting:
	default:
		return nil
	}
	if err := agentupdate.ConfirmReconnect(root, state.CommandID, version); err != nil {
		return err
	}
	state.Stage = agentprotocol.StageReconnecting
	state.UpdatedAt = now().UTC()
	return sender.SendUpgradeEvent(ctx, agentprotocol.UpgradeEvent{
		CommandID: state.CommandID, TargetID: state.TargetID,
		Stage: state.Stage, OccurredAt: state.UpdatedAt,
	})
}

type applyUpgradeFunc func(string, string, agentupdate.SystemController) error

func runApplyUpgradeCommand(args []string, root string, system agentupdate.SystemController, apply applyUpgradeFunc) (bool, error) {
	if len(args) < 2 || args[1] != "apply-upgrade" {
		return false, nil
	}
	if len(args) != 3 {
		return true, fmt.Errorf("用法：yunling-agent apply-upgrade COMMAND_ID")
	}
	return true, apply(root, args[2], system)
}

func detectedCapabilities(goos string, stat func(string) (os.FileInfo, error)) []string {
	if goos != "linux" {
		return nil
	}
	info, err := stat("/etc/systemd/system/yunling-agent-upgrade@.service")
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	return []string{"self_upgrade_v1"}
}

func writeVersionCommand(args []string, output io.Writer) bool {
	if len(args) != 2 || args[1] != "version" {
		return false
	}
	fmt.Fprintln(output, agentVersion)
	return true
}

func newAgentLauncher(goos, mode string) executor.Launcher {
	if strings.EqualFold(strings.TrimSpace(mode), "process") {
		return executor.NewProcessLauncher()
	}
	if goos == "linux" {
		return executor.NewSystemdLauncher()
	}
	return executor.NewProcessLauncher()
}
