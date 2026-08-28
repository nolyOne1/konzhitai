package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfigRequiresDatabaseAndUsesSafeSchedulerDefaults(t *testing.T) {
	values := map[string]string{"YUNLING_DATABASE_URL": "postgres://scheduler", "YUNLING_REDIS_PASSWORD": "test-only"}
	config, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("读取调度器配置：%v", err)
	}
	if config.RedisAddress != "127.0.0.1:6379" || config.RedisDatabase != 0 || config.ScanInterval != 15*time.Second {
		t.Fatalf("调度器默认值错误：%+v", config)
	}

	_, err = loadConfig(func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "数据库") {
		t.Fatalf("缺少数据库地址应返回中文错误，实际=%v", err)
	}
}
