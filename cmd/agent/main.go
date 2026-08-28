package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"yunling.local/platform/internal/agent"
	"yunling.local/platform/internal/executor"
)

const agentVersion = "0.1.0"

func main() {
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
	stats, err := agent.NewSystemStatsSource(diskPath, nil)
	if err != nil {
		log.Fatalf("初始化系统资源采集失败：%v", err)
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
	collector := agent.NewCollector(stats, runtimes)
	allowedRoots := agent.ParseAllowedScriptRoots(os.Getenv("YUNLING_ALLOWED_SCRIPT_ROOTS"))
	if len(allowedRoots) > 0 {
		discovered, err := executor.NewDiscovery().List(context.Background(), allowedRoots)
		if err != nil {
			log.Fatalf("扫描允许脚本目录失败：%v", err)
		}
		log.Printf("已从允许目录发现 %d 个可导入脚本", len(discovered))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sender, err := agent.DialHeartbeatSender(ctx, credentials.ControlURL, credentials.Credential)
	if err != nil {
		log.Fatalf("连接云令中央服务失败：%v", err)
	}
	defer sender.Close()

	heartbeatClient := agent.NewClient(credentials.ServerID, agentVersion, collector, sender)
	cacheRoot := filepath.Join(diskPath, "script-cache")
	cache := executor.NewCache(cacheRoot, agent.NewCredentialDownloader(credentials.Credential, nil))
	syncClient := agent.NewSyncClient(cache, executor.NewDriftScanner(cacheRoot), sender)
	log.Printf("云令代理已连接，服务器编号：%s", credentials.ServerID)
	errors := make(chan error, 2)
	go func() { errors <- heartbeatClient.Run(ctx) }()
	go func() { errors <- syncClient.Run(ctx) }()
	select {
	case <-ctx.Done():
		return
	case err := <-errors:
		if err != nil {
			log.Fatalf("云令代理停止：%v", err)
		}
	}
}
