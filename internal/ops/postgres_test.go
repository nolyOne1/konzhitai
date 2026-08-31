package ops

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/alert"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresRuleStateTransitionsRequireNewSamplesAndCAS(t *testing.T) {
	db := opsDatabase(t)
	repository := NewPostgresRepository(db)
	ctx := context.Background()
	firstAt := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	key := RuleKey{Code: "memory_low", SourceType: "server", SourceID: "server-1"}
	event := alert.Event{ResourceType: "server", ResourceID: "server-1", Code: "memory_low", Severity: alert.SeverityWarning, Title: "服务器可用内存不足"}
	low := 9.0
	first := Evaluation{Key: key, Bad: true, Value: &low, EvaluatedAt: firstAt, RequiredConsecutive: 2, SampleBased: true, Event: event}
	transitions, err := repository.Apply(ctx, []Evaluation{first, first})
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 0 {
		t.Fatal("同一资源样本不得重复计数")
	}
	second := first
	second.EvaluatedAt = firstAt.Add(15 * time.Second)
	transitions, err = repository.Apply(ctx, []Evaluation{second})
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 1 || !transitions[0].DesiredActive {
		t.Fatalf("第二个新样本应触发告警转换：%+v", transitions)
	}
	if err := repository.MarkApplied(ctx, key, true); err != nil {
		t.Fatal(err)
	}
	transitions, err = repository.Apply(ctx, []Evaluation{second})
	if err != nil || len(transitions) != 0 {
		t.Fatalf("已应用状态和重复样本不得重放：transitions=%+v err=%v", transitions, err)
	}

	high := 16.0
	goodOne := second
	goodOne.Bad, goodOne.Good, goodOne.Value = false, true, &high
	goodOne.EvaluatedAt = second.EvaluatedAt.Add(15 * time.Second)
	goodTwo := goodOne
	goodTwo.EvaluatedAt = goodOne.EvaluatedAt.Add(15 * time.Second)
	transitions, err = repository.Apply(ctx, []Evaluation{goodOne, goodTwo})
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 1 || transitions[0].DesiredActive {
		t.Fatalf("连续两个恢复样本应触发恢复转换：%+v", transitions)
	}
}

func TestPostgresRuleSnapshotLoadsLatestServerSamples(t *testing.T) {
	db := opsDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	var serverID string
	if err := db.QueryRow(ctx, `
		INSERT INTO servers (name, status, enabled, drain_requested, last_seen_at)
		VALUES ('执行节点', 'draining', true, true, $1) RETURNING id
	`, now).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		at := now.Add(time.Duration(index-2) * 15 * time.Second)
		if _, err := db.Exec(ctx, `
			INSERT INTO server_snapshots (
				server_id, cpu_usage_percent, memory_total_bytes, memory_available_bytes,
				disk_total_bytes, disk_available_bytes, running_tasks, collected_at
			) VALUES ($1, 10, 100, $2, 100, $3, 0, $4)
		`, serverID, 20+index, 30+index, at); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := NewPostgresRepository(db).Snapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Servers) != 1 || !snapshot.Servers[0].Draining || len(snapshot.Servers[0].Samples) != 2 {
		t.Fatalf("服务器规则快照不完整：%+v", snapshot.Servers)
	}
	if snapshot.Servers[0].Samples[0].MemoryAvailableBytes != 21 || snapshot.Servers[0].Samples[1].MemoryAvailableBytes != 22 {
		t.Fatalf("应按时间顺序返回最新两个快照：%+v", snapshot.Servers[0].Samples)
	}
}

func opsDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := testpostgres.Start(t)
	root := testpostgres.RepositoryRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		testpostgres.ApplyMigration(t, db, filepath.Base(path))
	}
	return db
}
