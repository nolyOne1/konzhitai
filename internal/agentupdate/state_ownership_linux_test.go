//go:build linux

package agentupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

func TestStateOwnershipMatchesDirectory(t *testing.T) {
	root := t.TempDir()
	spec := Spec{CommandID: "upgrade-ownership", TargetVersion: "0.2.1", Phase: "staged"}
	for range 2 {
		if err := saveSpec(root, spec); err != nil {
			t.Fatal(err)
		}
		if err := SaveRuntimeState(root, agentprotocol.UpgradeRuntimeState{CommandID: spec.CommandID, Stage: agentprotocol.StageInstalling}); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{filepath.Join(root, "runtime.json"), filepath.Join(root, spec.CommandID, "spec.json")} {
			parent, err := os.Stat(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			owner := parent.Sys().(*syscall.Stat_t)
			assertStateFileOwnership(t, path, owner.Uid, owner.Gid)
		}
		spec.Phase = "files_replaced"
	}
}

func TestStateOwnershipDoesNotFollowLegacyTemporarySymlinks(t *testing.T) {
	for _, kind := range []string{"spec", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			spec := Spec{CommandID: "upgrade-ownership", Phase: "staged"}
			if err := saveSpec(root, spec); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, spec.CommandID, "spec.json")
			write := func() error { return saveSpec(root, spec) }
			if kind == "runtime" {
				path = filepath.Join(root, "runtime.json")
				write = func() error {
					return SaveRuntimeState(root, agentprotocol.UpgradeRuntimeState{CommandID: spec.CommandID, Stage: agentprotocol.StageInstalling})
				}
			}
			victim := filepath.Join(root, "unrelated-file")
			if err := os.WriteFile(victim, []byte("must remain unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, path+".tmp"); err != nil {
				t.Fatal(err)
			}
			if err := write(); err != nil {
				t.Fatal(err)
			}
			if got := readTestFile(t, victim); got != "must remain unchanged" {
				t.Fatalf("legacy temporary symlink target was overwritten: %q", got)
			}
			if target, err := os.Readlink(path + ".tmp"); err != nil || target != victim {
				t.Fatalf("legacy symlink was modified: target=%q err=%v", target, err)
			}
		})
	}
}

// Run these cross-UID regressions with a precompiled binary so the unprivileged
// child does not need access to root's Go build cache or a named system account:
// go test -c -o /tmp/agentupdate.test ./internal/agentupdate
// sudo env YUNLING_TEST_REQUIRE_ROOT=1 /tmp/agentupdate.test -test.run '^TestStateOwnershipRoot' -test.v
func TestStateOwnershipRootReplacementAllowsAgentReconnect(t *testing.T) {
	fixture := newStateOwnershipRootFixture(t)
	spec := fixture.spec
	spec.Phase = "restarted"
	if err := saveSpec(fixture.root, spec); err != nil {
		t.Fatal(err)
	}
	if err := SaveRuntimeState(fixture.root, fixture.state); err != nil {
		t.Fatal(err)
	}
	fixture.assertFiles(t)
	fixture.runAgent(t, "reconnect")
	fixture.assertFiles(t)
	state, err := LoadRuntimeState(fixture.root)
	if err != nil || state == nil || state.Stage != agentprotocol.StageReconnecting {
		t.Fatalf("agent did not confirm reconnect: state=%+v err=%v", state, err)
	}
	if got := readTestFile(t, filepath.Join(fixture.root, spec.CommandID, "connected")); got != "0.2.1\n" {
		t.Fatalf("wrong reconnect marker: %q", got)
	}
}

func TestStateOwnershipRootAutomaticRollbackRemainsAgentReadable(t *testing.T) {
	fixture := newStateOwnershipRootFixture(t)
	spec := fixture.spec
	spec.Phase = "rollback_ready"
	if err := saveSpec(fixture.root, spec); err != nil {
		t.Fatal(err)
	}
	system := &fakeSystem{restartHook: func(int) {
		// The old unprivileged agent must read the failure before restarting,
		// not merely after the root helper has completed its remaining writes.
		fixture.runAgent(t, "rollback_ready")
	}}
	cause := errors.New("new agent failed to reconnect")
	if err := finishAutomaticRollback(fixture.root, &spec, system, cause); !errors.Is(err, cause) {
		t.Fatalf("automatic rollback lost original failure: %v", err)
	}
	if system.restarts != 1 {
		t.Fatalf("expected one old-agent restart, got %d", system.restarts)
	}
	fixture.assertFiles(t)
	fixture.runAgent(t, "rolled_back")
}

// Only invoked by a root test through exec.Cmd.Credential; no test changes the
// UID of the Go test parent, which can have other concurrently running tests.
func TestStateOwnershipAgentProcess(t *testing.T) {
	mode := os.Getenv("YUNLING_STATE_OWNERSHIP_HELPER")
	if mode == "" {
		t.Skip("unprivileged subprocess helper")
	}
	if os.Geteuid() != 65534 || os.Getegid() != 65534 {
		t.Fatalf("helper must genuinely run as UID/GID 65534: %d:%d", os.Geteuid(), os.Getegid())
	}
	root := os.Getenv("YUNLING_STATE_OWNERSHIP_ROOT")
	state, err := LoadRuntimeState(root)
	if err != nil || state == nil || state.CommandID != "upgrade-ownership" || state.TargetID != "target-ownership" {
		t.Fatalf("unprivileged agent cannot read runtime state: state=%+v err=%v", state, err)
	}
	if mode == "reconnect" {
		if err := ConfirmReconnect(root, state.CommandID, "0.2.1"); err != nil {
			t.Fatalf("unprivileged reconnect failed: %v", err)
		}
		return
	}
	if mode != "rollback_ready" && mode != "rolled_back" {
		t.Fatalf("unsupported helper mode %q", mode)
	}
	if state.Stage != agentprotocol.StageFailed || state.ErrorCode != "apply_rolled_back" {
		t.Fatalf("old agent did not receive rollback outcome: %+v", state)
	}
	spec, err := loadSpec(root, state.CommandID)
	if err != nil || spec.Phase != mode {
		t.Fatalf("old agent cannot read rollback spec: spec=%+v err=%v", spec, err)
	}
}

type stateOwnershipRootFixture struct {
	root, executable string
	spec             Spec
	state            agentprotocol.UpgradeRuntimeState
}

func newStateOwnershipRootFixture(t *testing.T) stateOwnershipRootFixture {
	t.Helper()
	if os.Geteuid() != 0 {
		if os.Getenv("YUNLING_TEST_REQUIRE_ROOT") == "1" {
			t.Fatal("YUNLING_TEST_REQUIRE_ROOT=1 requires root for cross-UID regression tests")
		}
		t.Skip("requires root; CI runs a precompiled test binary with sudo")
	}
	// t.TempDir and the original executable may live below non-traversable root
	// directories. Keep this fixture directly in /tmp and copy only this binary.
	base, err := os.MkdirTemp("/tmp", "yunling-state-ownership-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Errorf("remove isolated ownership fixture: %v", err)
		}
	})
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := stateOwnershipRootFixture{
		root: filepath.Join(base, "upgrades"), executable: filepath.Join(base, "agentupdate.test"),
		spec:  Spec{CommandID: "upgrade-ownership", TargetID: "target-ownership", SourceVersion: "0.2.0", TargetVersion: "0.2.1"},
		state: agentprotocol.UpgradeRuntimeState{CommandID: "upgrade-ownership", TargetID: "target-ownership", TargetVersion: "0.2.1", Stage: agentprotocol.StageInstalling},
	}
	for _, dir := range []string{fixture.root, filepath.Join(fixture.root, fixture.spec.CommandID)} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(dir, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	for path, value := range map[string]any{
		filepath.Join(fixture.root, "runtime.json"):                      fixture.state,
		filepath.Join(fixture.root, fixture.spec.CommandID, "spec.json"): fixture.spec,
	} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		// Reproduce already-broken legacy files, not just first-time writes.
		if err := os.Chown(path, 0, 0); err != nil {
			t.Fatal(err)
		}
		assertStateFileOwnership(t, path, 0, 0)
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(current)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(fixture.executable, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o555)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	if err := errors.Join(copyErr, output.Close()); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture stateOwnershipRootFixture) assertFiles(t *testing.T) {
	t.Helper()
	assertStateFileOwnership(t, filepath.Join(fixture.root, "runtime.json"), 65534, 65534)
	assertStateFileOwnership(t, filepath.Join(fixture.root, fixture.spec.CommandID, "spec.json"), 65534, 65534)
}

func (fixture stateOwnershipRootFixture) runAgent(t *testing.T, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, fixture.executable, "-test.run=^TestStateOwnershipAgentProcess$", "-test.v")
	command.Dir = filepath.Dir(fixture.root)
	command.Env = append(os.Environ(), "YUNLING_STATE_OWNERSHIP_HELPER="+mode, "YUNLING_STATE_OWNERSHIP_ROOT="+fixture.root)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("agent UID 65534 subprocess failed (%s): %v\n%s", mode, err, output)
	}
}

func assertStateFileOwnership(t *testing.T, path string, uid, gid uint32) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0o600 || owner.Uid != uid || owner.Gid != gid {
		t.Fatalf("incorrect state ownership for %s: mode=%o owner=%d:%d want=600 %d:%d", path, info.Mode().Perm(), owner.Uid, owner.Gid, uid, gid)
	}
}
