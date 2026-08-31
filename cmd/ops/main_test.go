package main

import (
	"testing"
	"time"
)

func TestConfigRequiresDatabaseAndMasterKeyAndDefaultsToFifteenSeconds(t *testing.T) {
	config, err := loadConfig(mapEnv(map[string]string{
		"YUNLING_DATABASE_URL":    "postgres://example",
		"YUNLING_MASTER_KEY_FILE": "/run/secrets/yunling-master-key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.ScanInterval != 15*time.Second || config.HTTPAddress != ":8081" || config.MasterKeyVersion != 1 {
		t.Fatalf("默认配置错误：%+v", config)
	}
	for _, values := range []map[string]string{
		{"YUNLING_MASTER_KEY_FILE": "/run/secrets/yunling-master-key"},
		{"YUNLING_DATABASE_URL": "postgres://example"},
	} {
		if _, err := loadConfig(mapEnv(values)); err == nil {
			t.Fatalf("缺少必要配置必须失败：%+v", values)
		}
	}
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
