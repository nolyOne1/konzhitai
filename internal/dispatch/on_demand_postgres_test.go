package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/dispatch"
	"yunling.local/platform/internal/scheduler"
	"yunling.local/platform/internal/script"
	"yunling.local/platform/internal/task"
)

func TestOnDemandDispatchSynchronizesPinnedVersionBeforeExecution(t *testing.T) {
	db := dispatchDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	seedDispatchRuns(t, db, now)
	// A newer version and another compatible server must not change this run's
	// pinned artifact or cause on-demand fan-out.
	const newerVersion = "20000000-0000-4000-8000-000000000007"
	const otherServer = "20000000-0000-4000-8000-000000000008"
	for _, query := range []string{
		`UPDATE task_runs SET state='queued', assigned_server_id=NULL, assigned_at=NULL, execution_token=NULL WHERE id='` + dispatchDueRunID + `'`,
		`UPDATE task_definitions SET secret_bindings = '{}'`,
		`UPDATE task_runs SET state = 'cancelled' WHERE id = '` + dispatchRecentRunID + `'`,
		`INSERT INTO script_versions (id,script_id,version,artifact_uri,artifact_sha256,entrypoint,manifest)
		 VALUES ('` + newerVersion + `','` + dispatchScriptID + `',2,'new.tar.gz',repeat('b',64),'main.sh','{"runtime":"bash","distribution":{"mode":"on_demand"}}')`,
		`INSERT INTO servers (id,name,status,runtimes) VALUES ('` + otherServer + `','other','online','["bash"]')`,
	} {
		if _, err := db.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	clock := func() time.Time { return now }
	syncService := script.NewSyncService(db, "https://control.example", clock)
	if count, err := syncService.PrepareVersion(ctx, dispatchVersionID); err != nil || count != 0 {
		t.Fatalf("on-demand publish must not pre-distribute: count=%d err=%v", count, err)
	}
	sender, failures := &fakeCommandSender{}, &fakeFailureSink{}
	service := dispatch.NewService(dispatch.NewPostgresStore(db), sender, nil, failures, clock)
	schedulerStore := scheduler.NewPostgresStore(db)
	assignment := scheduler.Assignment{
		RunID: dispatchDueRunID, ServerID: dispatchServerID, ScriptVersionID: newerVersion,
		ExecutionToken: "token-due", AssignedAt: now,
		Lease: scheduler.Lease{ID: "20000000-0000-4000-8000-000000000009", ExpiresAt: now.Add(time.Hour), Resources: task.Resources{CPUMillicores: 100, MemoryBytes: 64 << 20, DiskBytes: 16 << 20}},
	}
	if assigned, err := schedulerStore.Assign(ctx, assignment); err != nil || assigned {
		t.Fatalf("cold run assigned: %v %v", assigned, err)
	}
	var state string
	var leases int
	if err := db.QueryRow(ctx, `SELECT state FROM task_runs WHERE id=$1`, dispatchDueRunID).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM resource_leases`).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("cold cache retained lease: %d %v", leases, err)
	}
	if err := service.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 0 || len(failures.events) != 0 {
		t.Fatal("cold cache must wait without starting or failing")
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM script_syncs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("only selected node/version should be prepared: count=%d err=%v", count, err)
	}
	command, ok, err := syncService.NextCommand(ctx, dispatchServerID)
	if err != nil || !ok || command.VersionID != dispatchVersionID || command.ScriptID != dispatchScriptID || command.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("pinned sync command=%+v ok=%v err=%v", command, ok, err)
	}
	if command.ArtifactURL != "https://control.example/api/agent/scripts/"+dispatchVersionID+"/artifact" {
		t.Fatalf("artifact URL=%s", command.ArtifactURL)
	}
	if _, ok, err := syncService.NextCommand(ctx, otherServer); err != nil || ok {
		t.Fatalf("unselected node got sync: ok=%v err=%v", ok, err)
	}
	now = now.Add(dispatch.DefaultRetryInterval)
	if assigned, err := schedulerStore.Assign(ctx, assignment); err != nil || assigned {
		t.Fatalf("downloading run assigned: %v %v", assigned, err)
	}
	if err := service.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 0 {
		t.Fatal("must not start during download")
	}
	if _, ok, err := syncService.NextCommand(ctx, dispatchServerID); err != nil || ok {
		t.Fatalf("dispatch reset an in-progress download: ok=%v err=%v", ok, err)
	}
	if err := syncService.RecordResult(ctx, dispatchServerID, agentprotocol.SyncResult{
		ScriptID: command.ScriptID, VersionID: command.VersionID, SHA256: command.SHA256, State: agentprotocol.SyncReady,
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(dispatch.DefaultRetryInterval)
	if assigned, err := schedulerStore.Assign(ctx, assignment); err != nil || !assigned {
		t.Fatalf("ready run not assigned: %v %v", assigned, err)
	}
	if err := service.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 || sender.command.Assignment.ScriptVersionID != dispatchVersionID || sender.command.Assignment.ExecutionToken != "token-due" {
		t.Fatalf("ready version not dispatched correctly: calls=%d command=%+v", sender.calls, sender.command)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM script_syncs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate/fanout sync: %d %v", count, err)
	}
}

func TestDispatchClaimPreservesSyncStateAndVerifiesChecksum(t *testing.T) {
	db := dispatchDatabase(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedDispatchRuns(t, db, now)
	store := dispatch.NewPostgresStore(db)
	runs, err := store.Claim(ctx, now.Add(-dispatch.DefaultRetryInterval), now, 20)
	if err != nil || len(runs) != 1 {
		t.Fatalf("initial claim=%+v %v", runs, err)
	}
	if runs[0].SyncState != "" || runs[0].ScriptVerified {
		t.Fatalf("cold state=%+v", runs[0])
	}
	if _, err := db.Exec(ctx, `INSERT INTO script_syncs(server_id,script_version_id,status) VALUES($1,$2,'pending')`, dispatchServerID, dispatchVersionID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		state, sha string
		verified   bool
	}{
		{"downloading", "", false}, {"failed", "", false}, {"drifted", "", false},
		{"ready", strings.Repeat("b", 64), false}, {"ready", strings.Repeat("a", 64), true},
	} {
		if _, err := db.Exec(ctx, `UPDATE script_syncs SET status=$1, artifact_sha256=NULLIF($2,''), error_message='keep diagnostic'`, tc.state, tc.sha); err != nil {
			t.Fatal(err)
		}
		now = now.Add(dispatch.DefaultRetryInterval)
		runs, err = store.Claim(ctx, now.Add(-dispatch.DefaultRetryInterval), now, 20)
		if err != nil || len(runs) == 0 {
			t.Fatalf("claim=%+v %v", runs, err)
		}
		for _, run := range runs {
			if string(run.SyncState) != tc.state || run.ScriptVerified != tc.verified {
				t.Fatalf("sync reset or unverified: %+v", run)
			}
		}
		var diagnostic string
		if err := db.QueryRow(ctx, `SELECT error_message FROM script_syncs WHERE server_id=$1 AND script_version_id=$2`, dispatchServerID, dispatchVersionID).Scan(&diagnostic); err != nil || diagnostic != "keep diagnostic" {
			t.Fatalf("diagnostic=%q %v", diagnostic, err)
		}
	}
}
