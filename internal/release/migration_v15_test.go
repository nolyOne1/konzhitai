package release

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationRolloutV15AppliesAllPendingSQLAndResumes(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "从12升级", true: "15提交后恢复基线"}[resumed], func(t *testing.T) {
			tree := candidateMigrationTree(t)
			history := historicalMigrationTreeDigest(t, tree)
			for _, name := range []string{"000014_agent_upgrade_management.up.sql", "000014_agent_upgrade_management.down.sql", "000015_agent_upgrade_recovery.up.sql", "000015_agent_upgrade_recovery.down.sql"} {
				body, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(tree, name), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			compatibility := validManifest().Compatibility
			compatibility.MigrationTreeSHA256 = history
			fixture := newDeploymentFixtureWithCompatibility(t, compatibility)
			digest, err := MigrationTreeDigest(tree)
			if err != nil {
				t.Fatal(err)
			}
			fixture.manifest.Compatibility.MigrationTreeSHA256 = digest
			responses := [][]byte{[]byte("12|t|7|3"), nil, []byte("15|t|t|t|7|3")}
			if resumed {
				responses = [][]byte{[]byte("15|t|7|3"), []byte("15|t|t|t|7|3")}
			}
			runner := &migrationRunner{responses: responses}
			rollout := &MigrationRollout{Config: fixture.config, Policy: fixture.deployer.Policy, Store: fixture.store, Runner: runner, Locker: &fakeLocker{}}
			err = rollout.Apply(context.Background(), MigrationRequest{Manifest: fixture.manifest, Actor: "release-admin", MigrationsDir: tree, RecoveryPointID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd"})
			if err != nil {
				t.Fatal(err)
			}
			if !resumed {
				body := string(runner.calls[1].stdin)
				previous := -1
				for _, marker := range []string{"BEGIN;", "ADD COLUMN must_change_password", "CREATE TABLE agent_releases", "ADD COLUMN install_command_id", "COMMIT;"} {
					index := strings.Index(body, marker)
					if index <= previous {
						t.Fatalf("迁移顺序错误：%s", marker)
					}
					previous = index
				}
			} else if len(runner.calls) != 2 {
				t.Fatal("恢复不得重放迁移SQL")
			}
			baseline, err := fixture.store.LoadMigrationBaseline("101")
			if err != nil || baseline.MigrationVersion != 15 {
				t.Fatalf("基线版本错误：%+v %v", baseline, err)
			}
		})
	}
}
