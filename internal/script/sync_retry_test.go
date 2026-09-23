package script_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/script"
	"yunling.local/platform/internal/testpostgres"
)

func TestSyncRetriesBackOffStopAtLimitAndRecoverAfterManualRetry(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000003_server_management.up.sql")
	testpostgres.ApplyMigration(t, db, "000004_script_sync_states.up.sql")
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	testpostgres.ApplyMigration(t, db, "000017_script_sync_retries.up.sql")
	ctx := context.Background()
	userID := insertUser(t, db)
	scriptID := insertScript(t, db, userID)
	version, err := script.NewService(db, newMemoryStore(), fixedClock).Publish(ctx, script.PublishInput{
		ScriptID: scriptID, Runtime: "bash", Entrypoint: "main.sh", Content: []byte("echo ok"),
		ReleaseNotes: "验证同步重试", Distribution: script.DistributionRule{Mode: script.DistributionAllCompatible}, AuthorID: userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	serverID := insertSyncServer(t, db, "重试节点", `["bash"]`, `{}`, true)
	now := fixedClock()
	service := script.NewSyncService(db, "https://control.example", func() time.Time { return now })
	failure := agentprotocol.SyncResult{ScriptID: scriptID, VersionID: version.ID, State: agentprotocol.SyncFailed, ErrorCode: "download_failed", ErrorMessage: "下载失败"}
	var syncID string
	for attempt := 1; attempt <= script.SyncFailureLimit; attempt++ {
		if _, ok, err := service.NextCommand(ctx, serverID); err != nil || !ok {
			t.Fatalf("attempt %d unavailable: %v", attempt, err)
		}
		if err := service.RecordResult(ctx, serverID, failure); err != nil {
			t.Fatal(err)
		}
		// A duplicate report must not consume the next attempt.
		if err := service.RecordResult(ctx, serverID, failure); err != nil {
			t.Fatal(err)
		}
		items, err := service.List(ctx, scriptID)
		if err != nil || len(items) != 1 {
			t.Fatalf("syncs=%+v error=%v", items, err)
		}
		item := items[0]
		syncID = item.ID
		if item.FailureCount != attempt || !item.Blocked || item.RetryLimit != 3 {
			t.Fatalf("bad failure state: %+v", item)
		}
		if _, ok, err := service.NextCommand(ctx, serverID); err != nil || ok {
			t.Fatalf("backoff ignored: ok=%v error=%v", ok, err)
		}
		if attempt < script.SyncFailureLimit {
			wantRetry := now.Add(time.Duration(30*(1<<(attempt-1))) * time.Second)
			if item.NextRetryAt == nil || !item.NextRetryAt.Equal(wantRetry) {
				t.Fatalf("next retry=%v want=%v", item.NextRetryAt, wantRetry)
			}
			now = wantRetry
		} else if item.NextRetryAt != nil {
			t.Fatalf("exhausted retry should stop: %+v", item)
		}
	}
	// A drift report cannot bypass an exhausted retry budget.
	if err := service.RecordResult(ctx, serverID, agentprotocol.SyncResult{ScriptID: scriptID, VersionID: version.ID, State: agentprotocol.SyncDrifted}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	if _, ok, err := service.NextCommand(ctx, serverID); err != nil || ok {
		t.Fatalf("exhausted retry dispatched: %v %v", ok, err)
	}
	if err := service.RetryScript(ctx, "wrong-script", syncID, userID); !errors.Is(err, script.ErrSyncNotFound) {
		t.Fatalf("cross-script retry accepted: %v", err)
	}
	if err := service.RetryScript(ctx, scriptID, syncID, userID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.NextCommand(ctx, serverID); err != nil || !ok {
		t.Fatalf("manual retry unavailable: %v", err)
	}
	if err := service.RecordResult(ctx, serverID, agentprotocol.SyncResult{ScriptID: scriptID, VersionID: version.ID, State: agentprotocol.SyncReady, SHA256: version.ArtifactSHA256}); err != nil {
		t.Fatal(err)
	}
	items, err := service.List(ctx, scriptID)
	if err != nil || items[0].FailureCount != 0 || items[0].NextRetryAt != nil || items[0].Blocked {
		t.Fatalf("success did not reset retry state: %+v %v", items, err)
	}
	var auditCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='script.sync_retry' AND actor_id=$1 AND target_id=$2`, userID, scriptID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("retry audit=%d error=%v", auditCount, err)
	}
}

func TestSyncMissingResultsConsumeRetryBudget(t *testing.T) {
	db := testpostgres.Start(t)
	for _, migration := range []string{"000001_initial.up.sql", "000003_server_management.up.sql", "000004_script_sync_states.up.sql", "000010_password_change_security.up.sql", "000017_script_sync_retries.up.sql"} {
		testpostgres.ApplyMigration(t, db, migration)
	}
	ctx := context.Background()
	userID := insertUser(t, db)
	scriptID := insertScript(t, db, userID)
	if _, err := script.NewService(db, newMemoryStore(), fixedClock).Publish(ctx, script.PublishInput{
		ScriptID: scriptID, Runtime: "bash", Entrypoint: "main.sh", Content: []byte("echo ok"),
		ReleaseNotes: "验证无回执重试", Distribution: script.DistributionRule{Mode: script.DistributionAllCompatible}, AuthorID: userID,
	}); err != nil {
		t.Fatal(err)
	}
	serverID := insertSyncServer(t, db, "无回执节点", `["bash"]`, `{}`, true)
	now := fixedClock()
	alerts := &syncAlertRecorder{}
	service := script.NewSyncService(db, "https://control.example", func() time.Time { return now }, script.WithAlertSink(alerts))
	for attempt := 1; attempt <= script.SyncFailureLimit; attempt++ {
		if _, ok, err := service.NextCommand(ctx, serverID); err != nil || !ok {
			t.Fatalf("attempt %d unavailable: %v", attempt, err)
		}
		// The agent disconnects without any SyncResult. Timeouts must consume
		// the same finite budget and cannot be immediately redispatched.
		now = now.Add(121 * time.Second)
		if _, ok, err := service.NextCommand(ctx, serverID); err != nil || ok {
			t.Fatalf("timeout bypassed backoff: %v %v", ok, err)
		}
		items, err := service.List(ctx, scriptID)
		if err != nil || len(items) != 1 {
			t.Fatalf("syncs=%+v error=%v", items, err)
		}
		item := items[0]
		if item.State != agentprotocol.SyncFailed || item.ErrorCode != "sync_timeout" || item.FailureCount != attempt || !item.Blocked {
			t.Fatalf("timeout budget not recorded: %+v", item)
		}
		// Repeated polling during backoff cannot count one timeout twice.
		if _, ok, err := service.NextCommand(ctx, serverID); err != nil || ok {
			t.Fatalf("polling timeout redispatched: %v %v", ok, err)
		}
		if attempt < script.SyncFailureLimit {
			want := now.Add(time.Duration(30*(1<<(attempt-1))) * time.Second)
			if item.NextRetryAt == nil || !item.NextRetryAt.Equal(want) {
				t.Fatalf("bad retry time: %+v", item)
			}
			now = want
		} else if item.NextRetryAt != nil {
			t.Fatalf("exhausted timeout still scheduled: %+v", item)
		}
	}
	now = now.Add(24 * time.Hour)
	if _, ok, err := service.NextCommand(ctx, serverID); err != nil || ok {
		t.Fatalf("fourth attempt was dispatched: %v %v", ok, err)
	}
	if len(alerts.events) != 3 || !strings.Contains(alerts.events[2].Message, "自动重试上限") {
		t.Fatalf("timeout limit alert missing: %+v", alerts.events)
	}
}
