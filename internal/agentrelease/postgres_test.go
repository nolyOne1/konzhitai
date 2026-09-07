package agentrelease

import (
	"context"
	"errors"
	"testing"

	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresRepositorySwitchesRecommendationAndPreservesImmutableLookup(t *testing.T) {
	db := testpostgres.Start(t)
	for _, migration := range []string{
		"000001_initial.up.sql", "000002_agent_enrollment.up.sql", "000003_server_management.up.sql",
		"000004_script_sync_states.up.sql", "000005_task_scheduling.up.sql", "000006_scheduler_resources.up.sql",
		"000007_run_observability.up.sql", "000008_security_audit_alerts.up.sql", "000009_run_dispatch.up.sql",
		"000010_password_change_security.up.sql", "000011_notifications.up.sql", "000012_backup_recovery.up.sql",
		"000013_member_lifecycle.up.sql", "000014_agent_upgrade_management.up.sql",
	} {
		testpostgres.ApplyMigration(t, db, migration)
	}
	repository := NewPostgresRepository(db)
	ctx := context.Background()
	a, err := repository.Create(ctx, persistedRelease("0.2.0", "a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := repository.Create(ctx, persistedRelease("0.3.0", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetRecommended(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetRecommended(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	recommended, err := repository.Recommended(ctx)
	if err != nil || recommended.ID != b.ID {
		t.Fatalf("推荐版本必须原子切换到 B：%+v err=%v", recommended, err)
	}
	if _, err := repository.Withdraw(ctx, b.ID); !errors.Is(err, ErrRecommendedRelease) {
		t.Fatalf("不得撤回推荐版本：%v", err)
	}
	withdrawn, err := repository.Withdraw(ctx, a.ID)
	if err != nil || withdrawn.Status != ReleaseStatusWithdrawn {
		t.Fatalf("撤回旧版本：%+v err=%v", withdrawn, err)
	}
	if _, err := repository.SetRecommended(ctx, a.ID); !errors.Is(err, ErrReleaseWithdrawn) {
		t.Fatalf("撤回版本不得设为推荐：%v", err)
	}
	artifact := b.Artifacts[0]
	if _, _, err := repository.FindArtifact(ctx, b.Version, artifact.SHA256, artifact.FileName); err != nil {
		t.Fatalf("精确查找安装包：%v", err)
	}
	if _, _, err := repository.FindArtifact(ctx, b.Version, artifact.SHA256, "other.tar.gz"); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("文件名不匹配必须失败：%v", err)
	}
}

func persistedRelease(version, seed string) Release {
	digest := ""
	for len(digest) < 64 {
		digest += seed
	}
	return Release{Version: version, Status: ReleaseStatusAvailable, ManifestSHA256: digest, Capabilities: []string{"self_upgrade_v1"}, Artifacts: []Artifact{
		{OS: "linux", Arch: "amd64", FileName: version + "-amd64.tar.gz", ByteSize: 10, SHA256: digest, ObjectKey: "agent-releases/" + version + "/" + digest + "/amd64.tar.gz"},
		{OS: "linux", Arch: "arm64", FileName: version + "-arm64.tar.gz", ByteSize: 11, SHA256: digest, ObjectKey: "agent-releases/" + version + "/" + digest + "/arm64.tar.gz"},
	}}
}
