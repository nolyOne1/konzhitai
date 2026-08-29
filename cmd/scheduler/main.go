package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"yunling.local/platform/internal/alert"
	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/store/postgres"
	redisstore "yunling.local/platform/internal/store/redis"
)

type config struct {
	DatabaseURL   string
	RedisAddress  string
	RedisPassword string
	RedisDatabase int
	ScanInterval  time.Duration
}

func main() {
	config, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	db, err := postgres.Open(connectCtx, config.DatabaseURL)
	cancel()
	if err != nil {
		log.Fatalf("连接调度数据库失败：%v", err)
	}
	defer db.Close()
	redisClient := goredis.NewClient(&goredis.Options{
		Addr: config.RedisAddress, Password: config.RedisPassword, DB: config.RedisDatabase,
	})
	defer redisClient.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = redisClient.Ping(pingCtx).Err()
	cancel()
	if err != nil {
		log.Fatalf("连接调度 Redis 失败：%v", err)
	}
	store := scheduler.NewPostgresStore(db)
	alertService := alert.NewService(alert.NewPostgresRepository(db), time.Now)
	service := scheduler.NewService(store, store, redisstore.NewLeaseStore(redisClient), time.Now, scheduler.WithAlertSink(alertService))
	log.Printf("云令调度器已启动，排队扫描间隔 %s", config.ScanInterval)
	if err := scan(ctx, service); err != nil {
		log.Printf("首次排队扫描失败：%v", err)
	}
	ticker := time.NewTicker(config.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Print("云令调度器已停止")
			return
		case <-ticker.C:
			if err := scan(ctx, service); err != nil {
				log.Printf("排队任务扫描失败：%v", err)
			}
		}
	}
}

type scanner interface{ Scan(context.Context) error }

func scan(parent context.Context, service scanner) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	return service.Scan(ctx)
}

func loadConfig(getenv func(string) string) (config, error) {
	result := config{
		DatabaseURL:   strings.TrimSpace(getenv("YUNLING_DATABASE_URL")),
		RedisAddress:  strings.TrimSpace(getenv("YUNLING_REDIS_ADDR")),
		RedisPassword: getenv("YUNLING_REDIS_PASSWORD"),
		ScanInterval:  15 * time.Second,
	}
	if result.DatabaseURL == "" {
		return config{}, errors.New("未设置调度数据库地址 YUNLING_DATABASE_URL")
	}
	if result.RedisAddress == "" {
		result.RedisAddress = "127.0.0.1:6379"
	}
	if value := strings.TrimSpace(getenv("YUNLING_REDIS_DB")); value != "" {
		database, err := strconv.Atoi(value)
		if err != nil || database < 0 {
			return config{}, fmt.Errorf("Redis 数据库编号无效：%s", value)
		}
		result.RedisDatabase = database
	}
	if value := strings.TrimSpace(getenv("YUNLING_SCHEDULER_SCAN_INTERVAL")); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil || interval <= 0 {
			return config{}, fmt.Errorf("调度扫描间隔无效：%s", value)
		}
		result.ScanInterval = interval
	}
	return result, nil
}
