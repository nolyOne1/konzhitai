package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMigrationRolloutAppliesV13AndAuthorizesOnlyBoundCandidate(t *testing.T) {
	migrations := candidateMigrationTree(t)
	compatibility := validManifest().Compatibility
	compatibility.MigrationTreeSHA256 = historicalMigrationTreeDigest(t, migrations)
	fixture := newDeploymentFixtureWithCompatibility(t, compatibility)
	targetDigest, err := MigrationTreeDigest(migrations)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manifest.Compatibility.MigrationTreeSHA256 = targetDigest
	runner := &migrationRunner{responses: [][]byte{[]byte("12|t|7|3\n"), nil, []byte("13|t|t|t|7|3\n")}}
	locker := &fakeLocker{}
	rollout := &MigrationRollout{
		Config: fixture.config, Policy: fixture.deployer.Policy, Store: fixture.store,
		Runner: runner, Locker: locker,
		Now: func() time.Time { return time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC) },
	}
	current, err := fixture.store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.deployer.resolveTarget(validDeployRequest(fixture.manifest), current); !errors.Is(err, ErrIncompatibleRelease) {
		t.Fatalf("迁移摘要变化在基线落盘前必须被标准发布链拒绝：%v", err)
	}

	err = rollout.Apply(context.Background(), MigrationRequest{
		Manifest: fixture.manifest, Actor: "release-admin", MigrationsDir: migrations,
		RecoveryPointID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if locker.released != 1 || len(runner.calls) != 3 {
		t.Fatalf("迁移锁或命令数错误：released=%d calls=%d", locker.released, len(runner.calls))
	}
	if body := string(runner.calls[1].stdin); !strings.Contains(body, "BEGIN;") ||
		!strings.Contains(body, "ADD COLUMN must_change_password") || !strings.Contains(body, "COMMIT;") {
		t.Fatalf("迁移必须在显式事务中执行候选 SQL：%s", body)
	}

	current, err = fixture.store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewStoredRelease(fixture.manifest, fixture.deployer.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if !fixture.store.MigrationBaselineAllows(current, target) {
		t.Fatal("已核验基线必须只授权对应候选")
	}
	other := target
	other.TargetID = "102"
	other.SourceSHA = strings.Repeat("e", 40)
	if fixture.store.MigrationBaselineAllows(current, other) {
		t.Fatal("迁移基线不得授权其他候选")
	}
	otherCompatibility := target
	otherCompatibility.Compatibility.DeploymentContractSHA256 = strings.Repeat("9", 64)
	if fixture.store.MigrationBaselineAllows(current, otherCompatibility) {
		t.Fatal("迁移基线不得授权迁移摘要之外的兼容性变化")
	}

	result, err := fixture.deployer.Execute(context.Background(), validDeployRequest(fixture.manifest))
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("显式迁移基线后发布链应接受候选：result=%+v err=%v", result, err)
	}
}

func TestMigrationRolloutRejectsTamperingAndNeverWritesUnverifiedBaseline(t *testing.T) {
	tests := []struct {
		name             string
		responses        [][]byte
		tamperTargetTree bool
		tamperHistory    bool
	}{
		{name: "目标摘要生成后候选迁移树被篡改", responses: [][]byte{[]byte("12|t|7|3\n")}, tamperTargetTree: true},
		{name: "候选重写既有迁移并同步更新目标摘要", responses: [][]byte{[]byte("12|t|7|3\n")}, tamperHistory: true},
		{name: "迁移后结构核验失败", responses: [][]byte{[]byte("12|t|7|3\n"), nil, []byte("13|t|f|t|7|3\n")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			migrations := candidateMigrationTree(t)
			compatibility := validManifest().Compatibility
			compatibility.MigrationTreeSHA256 = historicalMigrationTreeDigest(t, migrations)
			fixture := newDeploymentFixtureWithCompatibility(t, compatibility)
			if test.tamperHistory {
				if err := os.WriteFile(filepath.Join(migrations, "000001_initial.up.sql"), []byte("SELECT 'rewritten history';\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			targetDigest, err := MigrationTreeDigest(migrations)
			if err != nil {
				t.Fatal(err)
			}
			fixture.manifest.Compatibility.MigrationTreeSHA256 = targetDigest
			if test.tamperTargetTree {
				if err := os.WriteFile(filepath.Join(migrations, memberLifecycleMigrationFile), []byte("SELECT 13;\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &migrationRunner{responses: test.responses}
			rollout := &MigrationRollout{
				Config: fixture.config, Policy: fixture.deployer.Policy, Store: fixture.store,
				Runner: runner, Locker: &fakeLocker{}, Now: time.Now,
			}
			err = rollout.Apply(context.Background(), MigrationRequest{
				Manifest: fixture.manifest, Actor: "release-admin", MigrationsDir: migrations,
				RecoveryPointID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
			})
			if err == nil {
				t.Fatal("未核验迁移不得写入发布基线")
			}
			if test.tamperTargetTree || test.tamperHistory {
				if !errors.Is(err, ErrMigrationDigestMismatch) || len(runner.calls) != 0 {
					t.Fatalf("迁移摘要失败必须在访问数据库前拒绝：err=%v calls=%d", err, len(runner.calls))
				}
			}
			if _, err := fixture.store.LoadMigrationBaseline("101"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("失败迁移不得留下基线：%v", err)
			}
		})
	}
}

func TestSaveMigrationBaselineRecoversInterruptedFinalFile(t *testing.T) {
	for _, test := range []struct {
		name string
		body []byte
	}{
		{name: "空文件", body: []byte{}},
		{name: "截断 JSON", body: []byte(`{"schema_version":1,"target_id":"101"`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, baseline, path := newMigrationBaselineFixture(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.body, 0o600); err != nil {
				t.Fatal(err)
			}

			if err := store.SaveMigrationBaseline(baseline); err != nil {
				t.Fatalf("中断留下的最终文件必须可恢复重试：%v", err)
			}
			got, err := store.LoadMigrationBaseline(baseline.TargetID)
			if err != nil || !sameMigrationBaselineIdentity(got, baseline) {
				t.Fatalf("恢复后的基线无效：got=%+v err=%v", got, err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want, err := marshalJSONLine(baseline)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, want) {
				t.Fatalf("最终文件不是完整基线：got=%q want=%q", body, want)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Fatalf("最终基线权限必须为 0600：mode=%v", info.Mode().Perm())
				}
			}
		})
	}
}

func TestSaveMigrationBaselineIsIdempotentOnlyForSameIdentity(t *testing.T) {
	t.Run("完整同身份基线幂等成功", func(t *testing.T) {
		store, baseline, path := newMigrationBaselineFixture(t)
		if err := store.SaveMigrationBaseline(baseline); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveMigrationBaseline(baseline); err != nil {
			t.Fatalf("同身份重试必须幂等：%v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("幂等重试不得改写完整基线：err=%v", err)
		}
	})

	t.Run("完整不同身份基线保持不可变", func(t *testing.T) {
		store, baseline, path := newMigrationBaselineFixture(t)
		if err := store.SaveMigrationBaseline(baseline); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		other := baseline
		other.Actor = "other-admin"
		if err := store.SaveMigrationBaseline(other); !errors.Is(err, ErrReleaseExists) {
			t.Fatalf("不同身份不得覆盖已完成基线：%v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("冲突重试不得改写原基线：err=%v", err)
		}
	})
}

func TestSaveMigrationBaselinePublishFailureCleansTemporaryFile(t *testing.T) {
	store, baseline, path := newMigrationBaselineFixture(t)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveMigrationBaseline(baseline); !errors.Is(err, ErrReleaseExists) {
		t.Fatalf("非普通最终路径必须拒绝发布：%v", err)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".migration-baseline-101-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("发布失败不得遗留临时文件：%v", temporary)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("发布失败不得把非普通目标变成损坏文件：info=%v err=%v", info, err)
	}
}

func TestSaveMigrationBaselineNeverFollowsFinalSymlink(t *testing.T) {
	store, baseline, path := newMigrationBaselineFixture(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("当前平台不能创建符号链接：%v", err)
	}

	if err := store.SaveMigrationBaseline(baseline); !errors.Is(err, ErrReleaseExists) {
		t.Fatalf("符号链接最终路径必须拒绝发布：%v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "outside\n" {
		t.Fatalf("发布不得跟随或改写符号链接目标：body=%q err=%v", body, err)
	}
}

func TestMemberLifecycleMigrationArtifactAndRunbookAreBound(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "publish-candidate.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`install -d -m 0700 "$stage/migrations"`,
		`cp -a migrations/. "$stage/migrations/"`,
		`migrations`,
	} {
		if !bytes.Contains(workflow, []byte(required)) {
			t.Fatalf("候选 bootstrap 包未绑定迁移目录：缺少 %q", required)
		}
	}
	runbook, err := os.ReadFile(filepath.Join("..", "..", "deploy", "RELEASE.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"migration apply", "--recovery-point", "migration-baselines/<候选运行编号>.json",
		"不得执行 `000013_member_lifecycle.down.sql`",
	} {
		if !bytes.Contains(runbook, []byte(required)) {
			t.Fatalf("迁移操作手册缺少受控步骤 %q", required)
		}
	}
}

func newMigrationBaselineFixture(t *testing.T) (*StateStore, MigrationBaseline, string) {
	t.Helper()
	migrations := candidateMigrationTree(t)
	compatibility := validManifest().Compatibility
	compatibility.MigrationTreeSHA256 = historicalMigrationTreeDigest(t, migrations)
	fixture := newDeploymentFixtureWithCompatibility(t, compatibility)
	targetDigest, err := MigrationTreeDigest(migrations)
	if err != nil {
		t.Fatal(err)
	}
	migrationDigest, err := FileSHA256(filepath.Join(migrations, memberLifecycleMigrationFile))
	if err != nil {
		t.Fatal(err)
	}
	baseline := MigrationBaseline{
		SchemaVersion: migrationBaselineSchemaVersion, CurrentTargetID: "bootstrap",
		TargetID: "101", TargetSourceSHA: fixture.manifest.SourceSHA,
		FromMigrationTreeSHA256: compatibility.MigrationTreeSHA256,
		ToMigrationTreeSHA256:   targetDigest,
		MigrationVersion:        memberLifecycleMigration,
		MigrationFileSHA256:     migrationDigest,
		RecoveryPointID:         "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		Actor:                   "release-admin",
		AppliedAt:               time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC),
	}
	return fixture.store, baseline, filepath.Join(fixture.store.root, migrationBaselineDirectory, baseline.TargetID+".json")
}

type migrationRunner struct {
	calls     []commandCall
	responses [][]byte
}

func (runner *migrationRunner) Run(_ context.Context, name string, args []string, stdin []byte) (CommandResult, error) {
	runner.calls = append(runner.calls, commandCall{name: name, args: append([]string(nil), args...), stdin: append([]byte(nil), stdin...)})
	index := len(runner.calls) - 1
	if name != "docker" || index >= len(runner.responses) {
		return CommandResult{}, fmt.Errorf("未预期迁移命令：%s %v", name, args)
	}
	return CommandResult{Stdout: runner.responses[index]}, nil
}

func candidateMigrationTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("迁移目录不得包含子目录：%s", entry.Name())
		}
		body, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, entry.Name()), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join(root, memberLifecycleMigrationFile))
	if err != nil || !bytes.Contains(body, []byte("must_change_password")) {
		t.Fatal("测试迁移不是成员生命周期迁移")
	}
	return root
}

func historicalMigrationTreeDigest(t *testing.T, migrations string) string {
	t.Helper()
	history := t.TempDir()
	entries, err := os.ReadDir(migrations)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "000013_") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(migrations, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(history, entry.Name()), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := MigrationTreeDigest(history)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
