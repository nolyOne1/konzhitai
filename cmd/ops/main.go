package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"yunling.local/platform/internal/alert"
	"yunling.local/platform/internal/backup"
	"yunling.local/platform/internal/notification"
	"yunling.local/platform/internal/ops"
	"yunling.local/platform/internal/secret"
	"yunling.local/platform/internal/store/postgres"
)

type config struct {
	DatabaseURL      string
	MasterKeyFile    string
	MasterKeyVersion int
	HTTPAddress      string
	ScanInterval     time.Duration
	Backup           backup.Config
}

func loadConfig(getenv func(string) string) (config, error) {
	backupConfiguration, err := backup.LoadConfig(getenv)
	if err != nil {
		return config{}, err
	}
	value := config{
		DatabaseURL:      strings.TrimSpace(getenv("YUNLING_DATABASE_URL")),
		MasterKeyFile:    strings.TrimSpace(getenv("YUNLING_MASTER_KEY_FILE")),
		MasterKeyVersion: 1,
		HTTPAddress:      strings.TrimSpace(getenv("YUNLING_OPS_HTTP_ADDR")),
		ScanInterval:     15 * time.Second,
		Backup:           backupConfiguration,
	}
	if value.DatabaseURL == "" || value.MasterKeyFile == "" {
		return config{}, errors.New("必须配置数据库和主密钥文件")
	}
	if value.HTTPAddress == "" {
		value.HTTPAddress = ":8081"
	}
	if raw := strings.TrimSpace(getenv("YUNLING_MASTER_KEY_VERSION")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return config{}, errors.New("主密钥版本无效")
		}
		value.MasterKeyVersion = parsed
	}
	if raw := strings.TrimSpace(getenv("YUNLING_OPS_SCAN_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return config{}, errors.New("运维扫描间隔无效")
		}
		value.ScanInterval = parsed
	}
	return value, nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--check-tools" {
		if err := checkTools(context.Background(), os.Stdout, backup.NewCommandRunner(10*time.Second)); err != nil {
			log.Fatal("备份工具检查失败")
		}
		return
	}
	configuration, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	connectContext, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	pool, err := postgres.Open(connectContext, configuration.DatabaseURL)
	cancelConnect()
	if err != nil {
		log.Fatalf("连接运维数据库失败：%v", err)
	}
	defer pool.Close()

	keyProvider := secret.NewFileKeyProvider(configuration.MasterKeyFile, configuration.MasterKeyVersion)
	masterKey, err := keyProvider.Current(ctx)
	clear(masterKey.Material)
	if err != nil {
		log.Fatal("运维主密钥不可用")
	}
	secretService := secret.NewService(secret.NewPostgresRepository(pool), keyProvider)
	notificationRepository := notification.NewPostgresRepository(pool)
	alertService := alert.NewService(alert.NewPostgresRepository(pool), time.Now)
	backupRepository := backup.NewPostgresRepository(pool)
	backupRunner := backup.NewCommandRunner(configuration.Backup.CommandTimeout)
	backupPaths := backup.NewRunPaths(configuration.Backup.Root)
	resticRepository := backup.NewResticRepository(configuration.Backup, backupRunner)
	backupExporter := backup.NewExporter(configuration.Backup, backupRunner, backupPaths, backup.DeploymentMetadata{
		GitRevision: os.Getenv("YUNLING_GIT_REVISION"), MigrationVersion: "12", ImageDigests: map[string]string{},
	}, time.Now)
	backupVerifier := backup.NewVerifier(configuration.Backup, resticRepository, backupRunner, backupPaths)
	backupService := backup.NewService(backupRepository, backupExporter, resticRepository, time.Now).
		WithRemote(resticRepository, backup.NewRetention(resticRepository), alertService).
		WithVerifier(backupRepository, backupVerifier)
	ruleEngine := ops.NewRuleEngine(ops.NewPostgresRepository(pool), alertService, time.Now)
	outboxService := notification.NewOutboxService(
		notificationRepository,
		secretService,
		notification.NewFeishuClient(nil, time.Now),
		time.Now,
	)
	health := newHealthState(pool, time.Now)
	loop := ops.NewLoop(ruleEngine, outboxService, configuration.ScanInterval, 10*time.Second, health, func(err error) {
		log.Printf("运维扫描失败")
	}).WithBackup(backupService)

	server := &http.Server{
		Addr:              configuration.HTTPAddress,
		Handler:           health.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("云令 Ops 健康检查正在监听 %s", configuration.HTTPAddress)
		serverErrors <- server.ListenAndServe()
	}()
	loopErrors := make(chan error, 1)
	go func() { loopErrors <- loop.Run(ctx) }()

	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Ops 健康服务异常退出")
		}
	case err := <-loopErrors:
		if err != nil {
			log.Printf("Ops 扫描循环异常退出")
		}
	}
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(shutdownContext)
}

type toolRunner interface {
	Run(context.Context, string, []string, map[string]string) (backup.CommandResult, error)
}

func checkTools(ctx context.Context, output io.Writer, runner toolRunner) error {
	checks := []struct {
		name string
		path string
		args []string
	}{
		{name: "pg_dump", path: "/usr/bin/pg_dump", args: []string{"--version"}},
		{name: "pg_restore", path: "/usr/bin/pg_restore", args: []string{"--version"}},
		{name: "psql", path: "/usr/bin/psql", args: []string{"--version"}},
		{name: "mc", path: "/usr/bin/mc", args: []string{"--version"}},
		{name: "restic", path: "/usr/bin/restic", args: []string{"version"}},
	}
	for _, check := range checks {
		result, err := runner.Run(ctx, check.path, check.args, nil)
		if err != nil {
			return err
		}
		version := strings.TrimSpace(result.Stdout)
		if version == "" {
			version = strings.TrimSpace(result.Stderr)
		}
		if line, _, found := strings.Cut(version, "\n"); found {
			version = line
		}
		if _, err := fmt.Fprintf(output, "%s: %s\n", check.name, version); err != nil {
			return err
		}
	}
	return nil
}

type databasePinger interface {
	Ping(context.Context) error
}

type healthState struct {
	database databasePinger
	now      func() time.Time
	mutex    sync.RWMutex
	lastScan time.Time
}

func newHealthState(database databasePinger, now func() time.Time) *healthState {
	return &healthState{database: database, now: now}
}

func (h *healthState) MarkSuccessfulScan(at time.Time) {
	h.mutex.Lock()
	h.lastScan = at
	h.mutex.Unlock()
}

func (h *healthState) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		err := h.database.Ping(ctx)
		cancel()
		h.mutex.RLock()
		lastScan := h.lastScan
		h.mutex.RUnlock()
		if err != nil || lastScan.IsZero() || h.now().Sub(lastScan) > time.Minute {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
}
