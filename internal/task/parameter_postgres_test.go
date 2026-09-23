package task_test

import (
	"context"
	"errors"
	"testing"

	"yunling.local/platform/internal/task"
)

func TestTriggerValidatesResolvedVersionParametersBeforeCreatingRun(t *testing.T) {
	db := taskDatabase(t)
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	var first, second string
	for index, destination := range []*string{&first, &second} {
		manifest := `{"parameterDefinitions":[{"name":"数量","type":"number","required":true},{"name":"启用","type":"boolean","required":true}]}`
		if index == 0 {
			manifest = `{"parameterDefinitions":[{"name":"数量","type":"string","required":true}]}`
		}
		if err := db.QueryRow(ctx, `INSERT INTO script_versions(script_id,version,artifact_uri,artifact_sha256,entrypoint,manifest,created_by) VALUES($1,$2,'test-artifact',repeat('a',64),'main.sh',$3,$4) RETURNING id`, scriptID, index+1, manifest, userID).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	service := task.NewService(db, taskClock)
	input := validTaskInput(scriptID, userID, "参数校验")
	input.Parameters = map[string]any{"数量": float64(0), "启用": false}
	definition, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Trigger(ctx, definition.ID, task.Trigger{Parameters: map[string]any{"数量": "invalid"}}); !errors.Is(err, task.ErrInvalidParameters) {
		t.Fatalf("wrong override: %v", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM task_runs WHERE task_definition_id=$1`, definition.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid run persisted: count=%d err=%v", count, err)
	}
	run, err := service.Trigger(ctx, definition.ID, task.Trigger{})
	if err != nil || run.ScriptVersionID != second {
		t.Fatalf("latest manifest: run=%+v err=%v", run, err)
	}
	input.VersionPolicy = task.VersionPinned
	input.PinnedVersionID = first
	if _, err := service.Update(ctx, definition.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Trigger(ctx, definition.ID, task.Trigger{}); !errors.Is(err, task.ErrInvalidParameters) {
		t.Fatalf("pinned manifest must be honored: %v", err)
	}
	if _, err := service.Trigger(ctx, definition.ID, task.Trigger{Parameters: map[string]any{"数量": "零"}}); err != nil {
		t.Fatal(err)
	}
}
