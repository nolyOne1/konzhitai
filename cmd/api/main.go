package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/health"
	"yunling.local/platform/internal/store/postgres"
)

func main() {
	address := os.Getenv("YUNLING_HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}

	router := http.NewServeMux()
	router.Handle("GET /api/health", health.Handler())

	var authHandler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "认证服务尚未配置数据库"})
	})
	if dsn := os.Getenv("YUNLING_DATABASE_URL"); dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pool, err := postgres.Open(ctx, dsn)
		cancel()
		if err != nil {
			log.Fatalf("连接认证数据库失败：%v", err)
		}
		defer pool.Close()
		repository := auth.NewPostgresRepository(pool)
		authHandler = auth.Handler(auth.NewService(repository, repository))
	} else {
		log.Print("未设置 YUNLING_DATABASE_URL，认证接口将返回暂不可用")
	}
	router.Handle("/api/auth/", authHandler)

	log.Printf("云令 API 正在监听 %s", address)
	if err := http.ListenAndServe(address, router); err != nil {
		log.Fatal(err)
	}
}
