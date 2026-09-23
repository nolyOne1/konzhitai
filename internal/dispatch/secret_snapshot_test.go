package dispatch_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/dispatch"
	"yunling.local/platform/internal/secret"
	"yunling.local/platform/internal/task"
)

type snapshotSecretResolver struct{}

func (snapshotSecretResolver) ResolveForRun(_ context.Context, ids []secret.ID) (map[string]string, error) {
	values := map[string]string{}
	for _, id := range ids {
		values[string(id)] = "value-for-" + string(id)
	}
	return values, nil
}

func TestQueuedSecretSnapshotSurvivesTaskEditsAndRetry(t *testing.T) {
	db := dispatchDatabase(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedDispatchRuns(t, db, now)
	if _, err := db.Exec(ctx, `UPDATE task_definitions SET idempotent=true,max_retries=2 WHERE id=$1`, dispatchDefinitionID); err != nil {
		t.Fatal(err)
	}
	var requiredVersionID string
	if err := db.QueryRow(ctx, `INSERT INTO script_versions(script_id,version,artifact_uri,artifact_sha256,entrypoint,manifest)
		SELECT script_id,2,artifact_uri,artifact_sha256,entrypoint,manifest||'{"parameterDefinitions":[{"name":"访问令牌","type":"string","required":true}]}'::jsonb
		FROM script_versions WHERE id=$1 RETURNING id`, dispatchVersionID).Scan(&requiredVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE task_definitions SET pinned_version_id=$2 WHERE id=$1`, dispatchDefinitionID, requiredVersionID); err != nil {
		t.Fatal(err)
	}
	service := task.NewService(db, func() time.Time { return now })
	run, err := service.Trigger(ctx, dispatchDefinitionID, task.Trigger{Type: task.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE task_definitions SET secret_bindings='{"访问令牌":"secret-2"}' WHERE id=$1`, dispatchDefinitionID); err != nil {
		t.Fatal(err)
	}
	assertSnapshot := func(id string, expected string) {
		t.Helper()
		token := "snapshot-token-" + id
		if _, err := db.Exec(ctx, `UPDATE task_runs SET state='assigned',assigned_server_id=$2,execution_token=$4,assigned_at=$3 WHERE id=$1`, id, dispatchServerID, now, token); err != nil {
			t.Fatal(err)
		}
		runs, err := dispatch.NewPostgresStore(db).Claim(ctx, now.Add(time.Minute), now, 20)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, claimed := range runs {
			if claimed.ID == id {
				found = true
				if claimed.SecretBindings["访问令牌"] != expected || (expected == "" && len(claimed.SecretBindings) != 0) {
					t.Fatalf("dispatch used edited bindings: %+v", claimed.SecretBindings)
				}
			}
		}
		if !found {
			t.Fatal("snapshot run was not dispatched")
		}
		source := secret.NewRunValueSource(db, snapshotSecretResolver{})
		values, err := source.ValuesForRun(ctx, id, token)
		if err != nil {
			t.Fatal(err)
		}
		if expected == "" && len(values) != 0 || expected != "" && (len(values) != 1 || string(values[0]) != "value-for-"+expected) {
			t.Fatalf("redaction used edited bindings: %q", values)
		}
		if _, err := source.ValuesForRun(ctx, id, "wrong-token"); !errors.Is(err, secret.ErrRunAccessDenied) {
			t.Fatalf("bad execution token accepted: %v", err)
		}
	}
	assertSnapshot(run.ID, "secret-1")
	if _, err := db.Exec(ctx, `UPDATE task_runs SET state='failed',process_confirmed_gone=true,finished_at=$2 WHERE id=$1`, run.ID, now); err != nil {
		t.Fatal(err)
	}
	retryID, err := task.NewPostgresReconcileStore(db).RetryRun(ctx, task.RunID(run.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshot(string(retryID), "secret-1")

	// An explicitly empty snapshot must not fall back to newly added bindings.
	if _, err := db.Exec(ctx, `UPDATE task_definitions SET secret_bindings='{}',pinned_version_id=$2 WHERE id=$1`, dispatchDefinitionID, dispatchVersionID); err != nil {
		t.Fatal(err)
	}
	emptyRun, err := service.Trigger(ctx, dispatchDefinitionID, task.Trigger{Type: task.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE task_definitions SET secret_bindings='{"访问令牌":"secret-3"}' WHERE id=$1`, dispatchDefinitionID); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(emptyRun.ID, "")
}
