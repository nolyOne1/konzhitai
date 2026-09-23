package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func platformMigrationFixture(t *testing.T) (string, *deploymentFixture) {
	t.Helper()
	tree := t.TempDir()
	entries, err := os.ReadDir(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join("..", "..", "migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, entry.Name()), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	history, err := migrationTreeDigestThrough(tree, 15)
	if err != nil {
		t.Fatal(err)
	}
	compatibility := validManifest().Compatibility
	compatibility.MigrationTreeSHA256 = history
	fixture := newDeploymentFixtureWithCompatibility(t, compatibility)
	digest, err := MigrationTreeDigest(tree)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manifest.Compatibility.MigrationTreeSHA256 = digest
	return tree, fixture
}

func TestPlatformMigrationRolloutAppliesAndResumesExactCandidate(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "recover-after-commit"}[resumed], func(t *testing.T) {
			tree, fixture := platformMigrationFixture(t)
			responses := [][]byte{[]byte("15|t|7|3|f"), nil, []byte("19|t|t|t|7|3")}
			if resumed {
				responses = [][]byte{[]byte("19|t|7|3|t"), []byte("19|t|t|t|7|3")}
			}
			runner := &migrationRunner{responses: responses}
			rollout := &MigrationRollout{Config: fixture.config, Policy: fixture.deployer.Policy, Store: fixture.store, Runner: runner, Locker: &fakeLocker{}}
			err := rollout.Apply(context.Background(), MigrationRequest{Manifest: fixture.manifest, Actor: "release-admin", MigrationsDir: tree, RecoveryPointID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd"})
			if err != nil {
				t.Fatal(err)
			}
			if !resumed {
				body := string(runner.calls[1].stdin)
				last := -1
				for _, marker := range []string{"BEGIN;", "LOCK TABLE schema_migrations", "CREATE TABLE server_groups", "ADD COLUMN failure_count", "ALTER TABLE task_definitions", "ADD COLUMN archive_cursor", "INSERT INTO audit_logs", "COMMIT;"} {
					i := strings.Index(body, marker)
					if i <= last {
						t.Fatalf("wrong transaction ordering: %s", marker)
					}
					last = i
				}
				if strings.Contains(body, "ADD COLUMN must_change_password") {
					t.Fatal("15->19 must not replay old migrations")
				}
			} else if len(runner.calls) != 2 {
				t.Fatal("resumed migration replayed SQL")
			}
			baseline, err := fixture.store.LoadMigrationBaseline("101")
			if err != nil || baseline.MigrationVersion != 19 {
				t.Fatalf("baseline: %+v %v", baseline, err)
			}
			current, _ := fixture.store.LoadCurrent()
			target, _ := NewStoredRelease(fixture.manifest, fixture.deployer.Policy)
			if !fixture.store.MigrationBaselineAllows(current, target) {
				t.Fatal("exact candidate was not authorized")
			}
			target.SourceSHA = strings.Repeat("f", 40)
			if fixture.store.MigrationBaselineAllows(current, target) {
				t.Fatal("different candidate source was authorized")
			}
		})
	}
}

func TestPlatformMigrationRolloutRejectsUnboundStateAndTampering(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses [][]byte
		tamper    string
	}{
		{"wrong-start-version", [][]byte{[]byte("14|t|7|3|f")}, ""},
		{"backup-not-verified", [][]byte{[]byte("15|f|7|3|f")}, ""},
		{"different-candidate-receipt", [][]byte{[]byte("19|t|7|3|f")}, ""},
		{"schema-verification-failed", [][]byte{[]byte("15|t|7|3|f"), nil, []byte("19|t|t|f|7|3")}, ""},
		{"history-rewritten", nil, "history"},
		{"candidate-changed-after-manifest", nil, "candidate"},
		{"incomplete-tree", nil, "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree, fixture := platformMigrationFixture(t)
			if tc.tamper == "history" {
				if err := os.WriteFile(filepath.Join(tree, "000015_agent_upgrade_recovery.up.sql"), []byte("SELECT 15;\n"), 0600); err != nil {
					t.Fatal(err)
				}
				fixture.manifest.Compatibility.MigrationTreeSHA256, _ = MigrationTreeDigest(tree)
			}
			if tc.tamper == "candidate" {
				if err := os.WriteFile(filepath.Join(tree, "000016_server_groups.up.sql"), []byte("SELECT 16;\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.tamper == "missing" {
				if err := os.Remove(filepath.Join(tree, "000017_script_sync_retries.down.sql")); err != nil {
					t.Fatal(err)
				}
				fixture.manifest.Compatibility.MigrationTreeSHA256, _ = MigrationTreeDigest(tree)
			}
			runner := &migrationRunner{responses: tc.responses}
			rollout := &MigrationRollout{Config: fixture.config, Policy: fixture.deployer.Policy, Store: fixture.store, Runner: runner, Locker: &fakeLocker{}}
			if err := rollout.Apply(context.Background(), MigrationRequest{Manifest: fixture.manifest, Actor: "release-admin", MigrationsDir: tree, RecoveryPointID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd"}); err == nil {
				t.Fatal("unsafe migration accepted")
			}
			if tc.tamper != "" && len(runner.calls) > 0 {
				t.Fatal("tampered tree reached the database")
			}
			if _, err := fixture.store.LoadMigrationBaseline("101"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed migration left baseline: %v", err)
			}
		})
	}
}
