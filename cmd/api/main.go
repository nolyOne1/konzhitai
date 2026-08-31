package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/alert"
	"yunling.local/platform/internal/artifact"
	"yunling.local/platform/internal/audit"
	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/dispatch"
	"yunling.local/platform/internal/health"
	"yunling.local/platform/internal/logstream"
	"yunling.local/platform/internal/notification"
	"yunling.local/platform/internal/operationshttp"
	"yunling.local/platform/internal/script"
	"yunling.local/platform/internal/secret"
	"yunling.local/platform/internal/securityhttp"
	"yunling.local/platform/internal/server"
	"yunling.local/platform/internal/store/postgres"
	"yunling.local/platform/internal/task"
)

func main() {
	address := os.Getenv("YUNLING_HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}

	router := http.NewServeMux()
	router.Handle("GET /api/health", health.Handler())

	unavailableHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "服务尚未配置数据库"})
	})
	var authHandler http.Handler = unavailableHandler
	var serverHandler http.Handler = unavailableHandler
	var protectedServerHandler http.Handler = unavailableHandler
	var managementHandler http.Handler = unavailableHandler
	var scriptHandler http.Handler = unavailableHandler
	var taskHandler http.Handler = unavailableHandler
	var runHandler http.Handler = unavailableHandler
	var securityHandler http.Handler = unavailableHandler
	var passwordHandler http.Handler = unavailableHandler
	var operationsHandler http.Handler = unavailableHandler
	if dsn := os.Getenv("YUNLING_DATABASE_URL"); dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pool, err := postgres.Open(ctx, dsn)
		cancel()
		if err != nil {
			log.Fatalf("连接认证数据库失败：%v", err)
		}
		defer pool.Close()
		authRepository := auth.NewPostgresRepository(pool)
		authService := auth.NewService(authRepository, authRepository)
		auditService := audit.NewService(audit.NewPostgresRepository(pool), time.Now)
		alertService := alert.NewService(alert.NewPostgresRepository(pool), time.Now)
		authHandler = audit.Middleware(auditService)(auth.Handler(authService))
		protect := func(handler http.Handler) http.Handler {
			return auth.Authenticate(authService)(audit.Middleware(auditService)(handler))
		}
		passwordStore := auth.NewPostgresPasswordChangeStore(pool)
		passwordService := auth.NewPasswordChangeService(passwordStore, time.Now)
		passwordHandler = protect(auth.PasswordHandler(passwordService, os.Getenv("YUNLING_PUBLIC_URL")))
		taskService := task.NewService(pool, time.Now)
		taskHandler = protect(task.Handler(taskService))

		eventService := task.NewEventService(task.NewPostgresRunEventStore(pool))
		reconciler := task.NewReconciler(task.NewPostgresReconcileStore(pool), time.Now)
		serverRepository := server.NewPostgresRepository(pool)
		enrollment := server.NewEnrollmentService(serverRepository, time.Now)
		connections := server.NewAgentConnectionHub()
		registry := server.NewRegistry(
			serverRepository, time.Now,
			server.WithEventPublisher(offlineRunPublisher{reconciler: reconciler, alerts: alertService}),
			server.WithAlertSink(alertService),
		)
		logOptions := []logstream.ServiceOption{}
		var secretManager securityhttp.SecretManager
		var dispatchSecretResolver *secret.Service
		if keyPath := os.Getenv("YUNLING_MASTER_KEY_FILE"); keyPath != "" {
			keyVersion := 1
			if configured := strings.TrimSpace(os.Getenv("YUNLING_MASTER_KEY_VERSION")); configured != "" {
				parsed, parseErr := strconv.Atoi(configured)
				if parseErr != nil || parsed <= 0 {
					log.Printf("主密钥版本配置无效，敏感参数接口将返回暂不可用")
					keyPath = ""
				} else {
					keyVersion = parsed
				}
			}
			if keyPath != "" {
				keyProvider := secret.NewFileKeyProvider(keyPath, keyVersion)
				masterKey, err := keyProvider.Current(context.Background())
				clear(masterKey.Material)
				if err != nil {
					log.Printf("主密钥不可用，敏感参数接口将返回暂不可用：%v", err)
				} else {
					secretService := secret.NewService(secret.NewPostgresRepository(pool), keyProvider)
					secretManager = secretService
					dispatchSecretResolver = secretService
					operationsHandler = protect(operationshttp.NewHandler(operationshttp.Services{
						Notifications: notification.NewConfigService(notification.NewPostgresRepository(pool), secretService),
					}, os.Getenv("YUNLING_PUBLIC_URL")))
					logOptions = append(logOptions, logstream.WithRedaction(secret.NewRedactor(), secret.NewRunValueSource(pool, secretService)))
				}
			}
		} else {
			log.Print("未设置 YUNLING_MASTER_KEY_FILE，敏感参数接口将返回暂不可用")
		}
		logService := logstream.NewService(logstream.NewPostgresChunkStore(pool), logOptions...)
		agentOptions := []server.HandlerOption{
			server.WithConnectionHub(connections), server.WithRunEventReceiver(eventService),
			server.WithLogReceiver(apiLogReceiver{service: logService}), server.WithRunningReconciler(reconciler),
		}
		serverHandler = server.Handler(registry, enrollment, agentOptions...)
		management := server.NewManagementService(serverRepository, connections)
		managementHandler = protect(server.ManagementHandler(management))
		runService := task.NewRunService(pool, connections, reconciler, time.Now)
		runHandler = protect(task.RunHandler(runService))
		securityHandler = protect(securityhttp.NewHandler(securityhttp.Services{
			Secrets: secretManager, Audits: auditService, Alerts: alertService,
			Team:        auth.NewTeamService(authRepository),
			Credentials: server.NewCredentialService(serverRepository, connections, time.Now),
		}))

		secure, _ := strconv.ParseBool(os.Getenv("YUNLING_S3_SECURE"))
		objectStore, err := artifact.NewMinIOStore(artifact.MinIOConfig{
			Endpoint:  os.Getenv("YUNLING_S3_ENDPOINT"),
			AccessKey: os.Getenv("YUNLING_S3_ACCESS_KEY"),
			SecretKey: os.Getenv("YUNLING_S3_SECRET_KEY"),
			Bucket:    os.Getenv("YUNLING_S3_BUCKET"),
			Secure:    secure,
		})
		if err != nil {
			log.Printf("脚本对象存储尚未配置，脚本接口将返回暂不可用：%v", err)
		} else {
			scriptService := script.NewService(pool, objectStore, time.Now)
			syncService := script.NewSyncService(pool, publicBaseURL(address), time.Now, script.WithAlertSink(alertService))
			configuredAgentOptions := append([]server.HandlerOption{}, agentOptions...)
			configuredAgentOptions = append(configuredAgentOptions,
				server.WithSyncCoordinator(syncService),
				server.WithAgentArtifactProvider(script.NewVersionArtifactProvider(pool, objectStore)),
			)
			serverHandler = server.Handler(registry, enrollment, configuredAgentOptions...)
			scriptHandler = protect(script.Handler(scriptService, script.WithSyncManager(syncService)))
		}
		protectedServerHandler = auth.Authenticate(authService)(serverHandler)
		dispatchService := dispatch.NewService(
			dispatch.NewPostgresStore(pool),
			connections,
			dispatchSecretResolver,
			eventService,
			time.Now,
		)
		go dispatch.RunLoop(context.Background(), dispatchService, dispatch.DefaultScanInterval, func(err error) {
			log.Printf("任务派发扫描失败：%v", err)
		})
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := registry.ReconcileOffline(ctx)
				cancel()
				if err != nil {
					log.Printf("服务器离线对账失败：%v", err)
				}
			}
		}()
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for now := range ticker.C {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, err := taskService.ScheduleDue(ctx, now)
				cancel()
				if err != nil {
					log.Printf("定时计划扫描失败：%v", err)
				}
			}
		}()
	} else {
		log.Print("未设置 YUNLING_DATABASE_URL，认证和代理接口将返回暂不可用")
	}
	router.Handle("/api/auth/", authHandler)
	router.Handle("POST /api/auth/password", passwordHandler)
	router.Handle("/api/servers/enrollment-tokens", protectedServerHandler)
	router.Handle("/api/dashboard", managementHandler)
	router.Handle("/api/servers", managementHandler)
	router.Handle("/api/servers/{id}", managementHandler)
	router.Handle("/api/scripts", scriptHandler)
	router.Handle("/api/scripts/", scriptHandler)
	router.Handle("/api/tasks", taskHandler)
	router.Handle("/api/tasks/", taskHandler)
	router.Handle("/api/runs", runHandler)
	router.Handle("/api/runs/", runHandler)
	router.Handle("/api/secrets", securityHandler)
	router.Handle("/api/members", securityHandler)
	router.Handle("/api/members/", securityHandler)
	router.Handle("/api/audit", securityHandler)
	router.Handle("/api/alerts", securityHandler)
	router.Handle("/api/alerts/", securityHandler)
	router.Handle("/api/operations/", operationsHandler)
	router.Handle("/api/servers/{id}/credentials/rotate", securityHandler)
	router.Handle("/api/servers/{id}/credentials/revoke", securityHandler)
	router.Handle("/api/agent/", serverHandler)

	log.Printf("云令 API 正在监听 %s", address)
	if err := http.ListenAndServe(address, router); err != nil {
		log.Fatal(err)
	}
}

type offlineRunPublisher struct {
	reconciler *task.Reconciler
	alerts     *alert.Service
}

func (p offlineRunPublisher) Publish(ctx context.Context, event server.Event) error {
	if event.Type == "server.offline" {
		if err := p.reconciler.ServerOffline(ctx, event.ServerID); err != nil {
			return err
		}
		if p.alerts != nil {
			return p.alerts.Raise(ctx, alert.Event{
				ResourceType: "server", ResourceID: event.ServerID, Code: "agent_offline",
				Severity: alert.SeverityCritical, Title: "服务器离线", Message: "代理心跳已超过 15 秒未更新",
			})
		}
	}
	return nil
}

type apiLogReceiver struct{ service *logstream.Service }

func (r apiLogReceiver) Accept(ctx context.Context, chunk agentprotocol.LogChunk) (uint64, error) {
	return r.service.Accept(ctx, logstream.LogChunk{
		RunID: chunk.RunID, ExecutionToken: chunk.ExecutionToken, Sequence: chunk.Sequence,
		Stream: logstream.Stream(chunk.Stream), Content: chunk.Content, CreatedAt: chunk.CreatedAt,
	})
}

func publicBaseURL(address string) string {
	if configured := os.Getenv("YUNLING_PUBLIC_URL"); configured != "" {
		return configured
	}
	if address == "" || address[0] == ':' {
		return "http://127.0.0.1" + address
	}
	return "http://" + address
}
