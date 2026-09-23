package server

import (
	"context"
	"testing"

	"yunling.local/platform/internal/testpostgres"
)

func TestDashboardActivityUsesLatestVersionAndLiveRuns(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000004_script_sync_states.up.sql")
	ctx := context.Background()
	var scriptID, first, second, serverID, definitionID string
	for _, item := range []struct {
		sql  string
		args []any
		dest *string
	}{
		{`INSERT INTO scripts (name,runtime) VALUES ('订单同步','bash') RETURNING id`, nil, &scriptID},
	} {
		if err := db.QueryRow(ctx, item.sql, item.args...).Scan(item.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow(ctx, `INSERT INTO script_versions(script_id,version,artifact_uri,artifact_sha256,entrypoint) VALUES($1,1,'v1',repeat('a',64),'main.sh') RETURNING id`, scriptID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO script_versions(script_id,version,artifact_uri,artifact_sha256,entrypoint) VALUES($1,2,'v2',repeat('b',64),'main.sh') RETURNING id`, scriptID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO servers(name) VALUES('执行节点') RETURNING id`).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO script_syncs(server_id,script_version_id,status) VALUES($1,$2,'ready'),($1,$3,'failed')`, serverID, first, second); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO task_definitions(name,script_id,required_runtime,cpu_millicores,memory_bytes,disk_bytes,timeout_seconds) VALUES('同步任务',$1,'bash',100,1024,1024,60) RETURNING id`, scriptID).Scan(&definitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO task_runs(task_definition_id,script_version_id,trigger_type,state,assigned_server_id) VALUES($1,$2,'manual','running',$3),($1,$2,'manual','succeeded',$3)`, definitionID, second, serverID); err != nil {
		t.Fatal(err)
	}
	var dashboard Dashboard
	if err := NewPostgresRepository(db).dashboardActivity(ctx, &dashboard); err != nil {
		t.Fatal(err)
	}
	if len(dashboard.ActiveRuns) != 1 || dashboard.ActiveRuns[0].TaskName != "同步任务" || dashboard.ActiveRuns[0].ServerName != "执行节点" {
		t.Fatalf("activity: %+v", dashboard.ActiveRuns)
	}
	if dashboard.ScriptSync.PublishedScripts != 1 || dashboard.ScriptSync.Total != 1 || dashboard.ScriptSync.Ready != 0 || dashboard.ScriptSync.Failed != 1 {
		t.Fatalf("latest version: %+v", dashboard.ScriptSync)
	}
}
