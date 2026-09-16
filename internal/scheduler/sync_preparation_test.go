package scheduler_test

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/task"
)

func TestUnverifiedScriptRemainsQueuedWithoutRetainedLease(t *testing.T) {
	db := schedulerDatabase(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ids := seedSchedulingFixture(t, db, now)
	store := scheduler.NewPostgresStore(db)
	assignment := scheduler.Assignment{RunID: ids.runID, ServerID: ids.serverID, ScriptVersionID: ids.versionID, AssignedAt: now}
	for _, status := range []string{"pending", "downloading", "failed", "drifted", "ready"} {
		// Even a ready row is insufficient if its checksum does not match.
		if _, err := db.Exec(ctx, `UPDATE script_syncs SET status=$1,artifact_sha256=repeat('b',64),error_message='keep'`, status); err != nil {
			t.Fatal(err)
		}
		if assigned, err := store.Assign(ctx, assignment); err != nil || assigned {
			t.Fatalf("state=%s assigned=%v err=%v", status, assigned, err)
		}
		var state, syncStatus, message string
		if err := db.QueryRow(ctx, `SELECT state FROM task_runs WHERE id=$1`, ids.runID).Scan(&state); err != nil || state != "queued" {
			t.Fatalf("state=%s err=%v", state, err)
		}
		if err := db.QueryRow(ctx, `SELECT status,error_message FROM script_syncs WHERE server_id=$1 AND script_version_id=$2`, ids.serverID, ids.versionID).Scan(&syncStatus, &message); err != nil || syncStatus != status || message != "keep" {
			t.Fatalf("sync state overwritten: %s %s %v", syncStatus, message, err)
		}
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM resource_leases`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("lease=%d err=%v", count, err)
		}
	}
	// No agent command is necessary to cancel a run waiting for its cache.
	if err := task.NewRunService(db, nil, nil, func() time.Time { return now }).CancelRun(ctx, ids.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE script_syncs SET status='ready',artifact_sha256=repeat('a',64)`); err != nil {
		t.Fatal(err)
	}
	if assigned, err := store.Assign(ctx, assignment); err != nil || assigned {
		t.Fatalf("cancelled run resurrected: %v %v", assigned, err)
	}
}

func TestScriptPreparationDoesNotPreventQueueExpiry(t *testing.T) {
	db := schedulerDatabase(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ids := seedSchedulingFixture(t, db, now)
	if _, err := db.Exec(ctx, `UPDATE script_syncs SET status='pending'`); err != nil {
		t.Fatal(err)
	}
	store := scheduler.NewPostgresStore(db)
	assignment := scheduler.Assignment{RunID: ids.runID, ServerID: ids.serverID, AssignedAt: now}
	if assigned, err := store.Assign(ctx, assignment); err != nil || assigned {
		t.Fatalf("assigned=%v err=%v", assigned, err)
	}
	if expired, err := store.Expire(ctx, ids.runID, now.Add(time.Hour)); err != nil || !expired {
		t.Fatalf("expired=%v err=%v", expired, err)
	}
	if _, err := db.Exec(ctx, `UPDATE script_syncs SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	if assigned, err := store.Assign(ctx, assignment); err != nil || assigned {
		t.Fatalf("expired run resurrected: %v %v", assigned, err)
	}
}
