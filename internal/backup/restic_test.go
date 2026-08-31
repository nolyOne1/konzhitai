package backup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type resticFakeRunner struct {
	calls         []exportCommandCall
	repositoryNew bool
	snapshots     string
}

func (f *resticFakeRunner) Run(_ context.Context, name string, args []string, environment map[string]string) (CommandResult, error) {
	f.calls = append(f.calls, exportCommandCall{name: name, args: append([]string(nil), args...), env: environment})
	joined := strings.Join(args, " ")
	if strings.HasSuffix(joined, "cat config") && f.repositoryNew {
		return CommandResult{ExitCode: 10}, errors.New("repository missing")
	}
	if strings.Contains(joined, "snapshots --json") {
		return CommandResult{Stdout: f.snapshots, ExitCode: 0}, nil
	}
	return CommandResult{ExitCode: 0}, nil
}

func TestResticSnapshotLocalInitializesBacksUpFindsUniqueSnapshotAndChecks(t *testing.T) {
	runID := uuid.NewString()
	runner := &resticFakeRunner{repositoryNew: true, snapshots: `[{"id":"snapshot-one","tags":["backup-run=` + runID + `"]}]`}
	repository := NewResticRepository(Config{
		LocalRepositoryFile: "/run/config/local-repository",
		ResticPasswordFile:  "/run/secrets/restic-password",
	}, runner)
	snapshotID, err := repository.SnapshotLocal(context.Background(), "/var/lib/yunling-ops/staging/"+runID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotID != "snapshot-one" {
		t.Fatalf("快照 ID 错误：%q", snapshotID)
	}
	wantFragments := []string{
		"--repository-file /run/config/local-repository --password-file /run/secrets/restic-password cat config",
		"--repository-file /run/config/local-repository --password-file /run/secrets/restic-password init",
		"--repository-file /run/config/local-repository --password-file /run/secrets/restic-password backup /var/lib/yunling-ops/staging/" + runID + " --tag backup-run=" + runID,
		"--repository-file /run/config/local-repository --password-file /run/secrets/restic-password snapshots --json --tag backup-run=" + runID,
		"--repository-file /run/config/local-repository --password-file /run/secrets/restic-password check --read-data-subset=5%",
	}
	if len(runner.calls) != len(wantFragments) {
		t.Fatalf("Restic 调用数量错误：%+v", runner.calls)
	}
	for index, want := range wantFragments {
		got := strings.Join(runner.calls[index].args, " ")
		if got != want {
			t.Fatalf("第 %d 个 Restic 命令错误：got=%q want=%q", index+1, got, want)
		}
		if strings.Contains(got, "password-value") {
			t.Fatal("命令行不得出现密码")
		}
	}
}

func TestResticSnapshotLocalRejectsZeroOrMultipleMatchingSnapshots(t *testing.T) {
	for _, snapshots := range []string{`[]`, `[{"id":"one"},{"id":"two"}]`} {
		runner := &resticFakeRunner{snapshots: snapshots}
		repository := NewResticRepository(Config{
			LocalRepositoryFile: "/run/config/local-repository",
			ResticPasswordFile:  "/run/secrets/restic-password",
		}, runner)
		if _, err := repository.SnapshotLocal(context.Background(), "/staging", uuid.NewString()); err == nil {
			t.Fatalf("快照查询 %s 必须失败", snapshots)
		}
	}
}
