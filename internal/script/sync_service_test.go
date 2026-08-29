package script_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/alert"
	"yunling.local/platform/internal/script"
	"yunling.local/platform/internal/testpostgres"
)

func TestSyncServiceSelectsCompatibleServersAndRecoversDrift(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000003_server_management.up.sql")
	testpostgres.ApplyMigration(t, db, "000004_script_sync_states.up.sql")
	ctx := context.Background()
	userID := insertUser(t, db)
	scriptID := insertScript(t, db, userID)
	objects := newMemoryStore()
	version, err := script.NewService(db, objects, fixedClock).Publish(ctx, script.PublishInput{
		ScriptID: scriptID, Content: []byte("echo sync\n"), Runtime: "bash", Entrypoint: "main.sh",
		ReleaseNotes: "发布到所有兼容服务器", Distribution: script.DistributionRule{Mode: script.DistributionAllCompatible}, AuthorID: userID,
	})
	if err != nil {
		t.Fatalf("发布待同步版本：%v", err)
	}
	compatibleID := insertSyncServer(t, db, "兼容节点", `["bash","python3"]`, `{"用途":"批处理"}`, true)
	incompatibleID := insertSyncServer(t, db, "运行环境不兼容", `["python3"]`, `{}`, true)
	insertSyncServer(t, db, "已停用节点", `["bash"]`, `{}`, false)

	alerts := &syncAlertRecorder{}
	service := script.NewSyncService(db, "https://control.example", fixedClock, script.WithAlertSink(alerts))
	count, err := service.PrepareVersion(ctx, version.ID)
	if err != nil || count != 1 {
		t.Fatalf("应只为一台兼容服务器创建同步记录：count=%d err=%v", count, err)
	}
	command, ok, err := service.NextCommand(ctx, compatibleID)
	if err != nil || !ok {
		t.Fatalf("获取待同步命令：ok=%v err=%v", ok, err)
	}
	if command.ScriptID != scriptID || command.VersionID != version.ID || command.SHA256 != version.ArtifactSHA256 || command.ArtifactURL != "https://control.example/api/agent/scripts/"+version.ID+"/artifact" {
		t.Fatalf("同步命令不完整：%+v", command)
	}
	body, checksum, err := script.NewVersionArtifactProvider(db, objects).OpenVersionArtifact(ctx, compatibleID, version.ID)
	if err != nil || checksum != version.ArtifactSHA256 {
		t.Fatalf("已领取同步命令的服务器应可下载脚本包：checksum=%s err=%v", checksum, err)
	}
	_ = body.Close()
	if _, _, err := script.NewVersionArtifactProvider(db, objects).OpenVersionArtifact(ctx, incompatibleID, version.ID); err == nil {
		t.Fatal("未分配该版本的服务器不得下载脚本包")
	}
	items, err := service.List(ctx, scriptID)
	if err != nil || len(items) != 1 || items[0].State != agentprotocol.SyncDownloading {
		t.Fatalf("领取命令后状态必须为下载中：items=%+v err=%v", items, err)
	}
	staleService := script.NewSyncService(db, "https://control.example", func() time.Time { return fixedClock().Add(3 * time.Minute) })
	if _, ok, err = staleService.NextCommand(ctx, compatibleID); err != nil || !ok {
		t.Fatalf("连接中断留下的超时下载必须可重新领取：ok=%v err=%v", ok, err)
	}

	if err := service.RecordResult(ctx, compatibleID, agentprotocol.SyncResult{
		ScriptID: scriptID, VersionID: version.ID, State: agentprotocol.SyncDrifted,
		SHA256: version.ArtifactSHA256, ErrorCode: "content_mismatch", ErrorMessage: "服务器侧脚本已被修改",
	}); err != nil {
		t.Fatalf("记录漂移：%v", err)
	}
	if len(alerts.events) != 1 || alerts.events[0].ResourceType != "script_sync" || alerts.events[0].Code != "content_mismatch" {
		t.Fatalf("版本漂移必须生成中文系统告警：%+v", alerts.events)
	}
	items, err = service.List(ctx, scriptID)
	if err != nil || len(items) != 1 || items[0].State != agentprotocol.SyncDrifted || !items[0].Blocked {
		t.Fatalf("漂移版本必须保持阻断：items=%+v err=%v", items, err)
	}
	if _, ok, err = service.NextCommand(ctx, compatibleID); err != nil || !ok {
		t.Fatalf("漂移后必须自动进入重新同步：ok=%v err=%v", ok, err)
	}
	if err := service.RecordResult(ctx, compatibleID, agentprotocol.SyncResult{
		ScriptID: scriptID, VersionID: version.ID, State: agentprotocol.SyncReady, SHA256: version.ArtifactSHA256,
	}); err != nil {
		t.Fatalf("记录重新同步成功：%v", err)
	}
	items, err = service.List(ctx, scriptID)
	if err != nil || len(items) != 1 || items[0].State != agentprotocol.SyncReady || items[0].Blocked || items[0].SyncedAt == nil {
		t.Fatalf("校验就绪后必须解除阻断：items=%+v err=%v", items, err)
	}
	if _, ok, err = service.NextCommand(ctx, compatibleID); err != nil || ok {
		t.Fatalf("已就绪版本不得重复下发：ok=%v err=%v", ok, err)
	}
	newServerID := insertSyncServer(t, db, "后续新增节点", `["bash"]`, `{}`, true)
	newServerCommand, ok, err := service.NextCommand(ctx, newServerID)
	if err != nil || !ok || newServerCommand.VersionID != version.ID {
		t.Fatalf("新服务器上线后应自动补齐最新兼容版本：command=%+v ok=%v err=%v", newServerCommand, ok, err)
	}
}

type syncAlertRecorder struct{ events []alert.Event }

func (r *syncAlertRecorder) Raise(_ context.Context, event alert.Event) error {
	r.events = append(r.events, event)
	return nil
}

func TestSyncServiceAppliesLabelDistribution(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000003_server_management.up.sql")
	testpostgres.ApplyMigration(t, db, "000004_script_sync_states.up.sql")
	ctx := context.Background()
	userID := insertUser(t, db)
	scriptID := insertScript(t, db, userID)
	version, err := script.NewService(db, newMemoryStore(), fixedClock).Publish(ctx, script.PublishInput{
		ScriptID: scriptID, Content: []byte("print('sync')\n"), Runtime: "python3", Entrypoint: "main.py",
		ReleaseNotes: "按标签发布脚本", Distribution: script.DistributionRule{Mode: script.DistributionLabels, Labels: map[string]string{"用途": "批处理"}}, AuthorID: userID,
	})
	if err != nil {
		t.Fatalf("发布标签版本：%v", err)
	}
	insertSyncServer(t, db, "匹配节点", `["python3"]`, `{"用途":"批处理","机房":"华北"}`, true)
	insertSyncServer(t, db, "标签不匹配", `["python3"]`, `{"用途":"在线服务"}`, true)

	count, err := script.NewSyncService(db, "https://control.example/", fixedClock).PrepareVersion(ctx, version.ID)
	if err != nil || count != 1 {
		t.Fatalf("标签发布应只选择匹配节点：count=%d err=%v", count, err)
	}
}

func insertSyncServer(t *testing.T, db *pgxpool.Pool, name, runtimes, labels string, enabled bool) string {
	t.Helper()
	var id string
	err := db.QueryRow(context.Background(), `
		INSERT INTO servers (name, status, runtimes, labels, enabled)
		VALUES ($1, 'online', $2::jsonb, $3::jsonb, $4)
		RETURNING id
	`, name, runtimes, labels, enabled).Scan(&id)
	if err != nil {
		t.Fatalf("写入同步服务器：%v", err)
	}
	return id
}
