package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"yunling.local/platform/internal/artifact"
	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/health"
	"yunling.local/platform/internal/script"
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
		authHandler = auth.Handler(authService)
		taskService := task.NewService(pool, time.Now)
		taskHandler = auth.Authenticate(authService)(task.Handler(taskService))

		serverRepository := server.NewPostgresRepository(pool)
		registry := server.NewRegistry(serverRepository, time.Now)
		enrollment := server.NewEnrollmentService(serverRepository, time.Now)
		connections := server.NewAgentConnectionHub()
		serverHandler = server.Handler(registry, enrollment, server.WithConnectionHub(connections))
		management := server.NewManagementService(serverRepository, connections)
		managementHandler = auth.Authenticate(authService)(server.ManagementHandler(management))

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
			syncService := script.NewSyncService(pool, publicBaseURL(address), time.Now)
			serverHandler = server.Handler(
				registry, enrollment, server.WithConnectionHub(connections),
				server.WithSyncCoordinator(syncService),
				server.WithAgentArtifactProvider(script.NewVersionArtifactProvider(pool, objectStore)),
			)
			scriptHandler = auth.Authenticate(authService)(script.Handler(scriptService, script.WithSyncManager(syncService)))
		}
		protectedServerHandler = auth.Authenticate(authService)(serverHandler)
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
	router.Handle("/api/servers/enrollment-tokens", protectedServerHandler)
	router.Handle("/api/dashboard", managementHandler)
	router.Handle("/api/servers", managementHandler)
	router.Handle("/api/servers/{id}", managementHandler)
	router.Handle("/api/scripts", scriptHandler)
	router.Handle("/api/scripts/", scriptHandler)
	router.Handle("/api/tasks", taskHandler)
	router.Handle("/api/tasks/", taskHandler)
	router.Handle("/api/agent/", serverHandler)

	log.Printf("云令 API 正在监听 %s", address)
	if err := http.ListenAndServe(address, router); err != nil {
		log.Fatal(err)
	}
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
