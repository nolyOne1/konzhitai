package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

func TestSyncClientDownloadsCommandsAndReportsDriftEveryMinute(t *testing.T) {
	transport := &fakeSyncTransport{
		commands: make(chan agentprotocol.SyncCommand, 1),
		results:  make(chan agentprotocol.SyncResult, 2),
	}
	cache := &fakeSyncCache{}
	drift := &fakeDriftDetector{results: []agentprotocol.SyncResult{{
		ScriptID: "script-1", VersionID: "version-1", State: agentprotocol.SyncDrifted,
	}}}
	ticker := &fakeTicker{values: make(chan time.Time, 1), ready: make(chan time.Duration, 1)}
	client := NewSyncClient(cache, drift, transport, WithDriftTickerFactory(func(interval time.Duration) Ticker {
		ticker.ready <- interval
		return ticker
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	if interval := <-ticker.ready; interval != time.Minute {
		t.Fatalf("漂移扫描间隔必须为 60 秒，实际为 %s", interval)
	}
	command := agentprotocol.SyncCommand{
		ScriptID: "script-1", VersionID: "version-1", ArtifactURL: "https://control.example/artifact",
		SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	transport.commands <- command
	ready := receiveSyncResult(t, transport.results)
	if cache.command != command || ready.State != agentprotocol.SyncReady || ready.SHA256 != command.SHA256 {
		t.Fatalf("有效同步命令应缓存并返回就绪：cache=%+v result=%+v", cache.command, ready)
	}

	ticker.values <- time.Now()
	reportedDrift := receiveSyncResult(t, transport.results)
	if reportedDrift.State != agentprotocol.SyncDrifted || reportedDrift.ScriptID != "script-1" {
		t.Fatalf("周期扫描应上报漂移：%+v", reportedDrift)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消同步客户端应正常退出：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("同步客户端未在 1 秒内退出")
	}
}

func TestSyncClientReportsFailedWithoutReplacingCurrentVersion(t *testing.T) {
	transport := &fakeSyncTransport{commands: make(chan agentprotocol.SyncCommand, 1), results: make(chan agentprotocol.SyncResult, 1)}
	cache := &fakeSyncCache{err: errors.New("checksum mismatch")}
	client := NewSyncClient(cache, &fakeDriftDetector{}, transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	transport.commands <- agentprotocol.SyncCommand{ScriptID: "script-1", VersionID: "version-2", SHA256: "bad"}
	result := receiveSyncResult(t, transport.results)
	if result.State != agentprotocol.SyncFailed || result.ErrorCode != "sync_failed" || result.ErrorMessage == "" {
		t.Fatalf("缓存失败必须返回中文可诊断结果：%+v", result)
	}
}

func receiveSyncResult(t *testing.T, results <-chan agentprotocol.SyncResult) agentprotocol.SyncResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("1 秒内未收到脚本同步结果")
		return agentprotocol.SyncResult{}
	}
}

type fakeSyncCache struct {
	command agentprotocol.SyncCommand
	err     error
}

func (c *fakeSyncCache) Ensure(_ context.Context, command agentprotocol.SyncCommand) (string, error) {
	c.command = command
	return "C:/cache/main.sh", c.err
}

type fakeDriftDetector struct {
	results []agentprotocol.SyncResult
	err     error
}

func (d *fakeDriftDetector) Scan(context.Context) ([]agentprotocol.SyncResult, error) {
	return append([]agentprotocol.SyncResult(nil), d.results...), d.err
}

type fakeSyncTransport struct {
	commands chan agentprotocol.SyncCommand
	results  chan agentprotocol.SyncResult
}

func (t *fakeSyncTransport) ReceiveSyncCommand(ctx context.Context) (agentprotocol.SyncCommand, error) {
	select {
	case command := <-t.commands:
		return command, nil
	case <-ctx.Done():
		return agentprotocol.SyncCommand{}, ctx.Err()
	}
}

func (t *fakeSyncTransport) SendSyncResult(_ context.Context, result agentprotocol.SyncResult) error {
	t.results <- result
	return nil
}
