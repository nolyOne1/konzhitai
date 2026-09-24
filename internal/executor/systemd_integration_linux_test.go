//go:build linux && systemdintegration

package executor_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/executor"
)

const systemdCIWork = "/var/lib/yunling-agent/runs"
const systemdCICache = "/var/lib/yunling-agent/script-cache/scripts"

func requireSystemdCI(t *testing.T) {
	t.Helper()
	account, err := user.Current()
	if err != nil || account.Username != "yunling-agent" || os.Geteuid() == 0 || os.Getenv("YUNLING_TEST_SYSTEMD") != "1" {
		t.Fatal("real systemd acceptance requires the isolated CI fixture and unprivileged yunling-agent user")
	}
	pid1, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(pid1)) != "systemd" {
		t.Fatal("systemd must be PID 1; cannot skip acceptance")
	}
}

func TestSystemdRecoveryProbe(t *testing.T) {
	requireSystemdCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var empty bool
	var err error
	if os.Getenv("YUNLING_CI_WAIT_EMPTY") == "1" {
		empty, err = executor.WaitForNoActiveSystemdRuns(ctx)
	} else {
		empty, err = executor.NoActiveSystemdRuns(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	wantEmpty := os.Getenv("YUNLING_CI_EXPECT_EMPTY") == "1"
	if empty != wantEmpty {
		t.Fatalf("isolated task process probe: empty=%t, want=%t", empty, wantEmpty)
	}
}

func TestSystemdIsolatedAcceptance(t *testing.T) {
	requireSystemdCI(t)
	for _, tc := range []struct {
		name     string
		code     int
		terminal executor.EventType
		cancel   bool
	}{
		{"exit_zero", 0, executor.EventSucceeded, false},
		{"exit_seven", 7, executor.EventFailed, false},
		{"resource_usage", 0, executor.EventSucceeded, false},
		{"cancel_tree", -1, executor.EventCancelled, true},
		{"timeout_tree", -1, executor.EventTimedOut, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "ci-" + uuid.NewString()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			t.Cleanup(func() {
				cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
				defer stop()
				_ = exec.CommandContext(cleanupCtx, "/usr/bin/systemctl", "stop", "--no-ask-password", "yunling-run@"+id+".service").Run()
			})
			cache := filepath.Join(systemdCICache, id)
			if err := os.Mkdir(cache, 0750); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(cache, "main.sh")
			// Every real start increments a durable counter. The workload also
			// proves it cannot read the agent-owned replay journal.
			body := "#!/bin/bash\nset -eu\necho start >> launches\nid -un > identity\n" +
				"test ! -r ../.execution-records/" + id + ".claim.json\ntouch ready\n"
			if tc.code >= 0 {
				if tc.name == "resource_usage" {
					body += "sleep 7\n"
				}
				body += "exit " + strconv.Itoa(tc.code) + "\n"
			} else {
				body += "sleep 300 &\necho $! > child.pid\nwait\n"
			}
			if err := os.WriteFile(script, []byte(body), 0640); err != nil {
				t.Fatal(err)
			}
			a := agentprotocol.Assignment{RunID: id, ExecutionToken: uuid.NewString(), ScriptVersionID: uuid.NewString(), Runtime: "bash", ScriptPath: script,
				Timeout: 20 * time.Second, Resources: agentprotocol.ResourceLimits{CPUMillicores: 100, MemoryBytes: 128 << 20, DiskBytes: 128 << 20, TasksMax: 32}}
			if tc.terminal == executor.EventTimedOut {
				a.Timeout = 3 * time.Second
			}
			runner := executor.NewRunner(executor.NewSystemdLauncher(), time.Second)
			events, err := runner.Start(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(systemdCIWork, id)
			if tc.cancel {
				awaitCIFile(t, ctx, filepath.Join(work, "child.pid"))
				if err := runner.Cancel(ctx, id, a.ExecutionToken); err != nil {
					t.Fatal(err)
				}
			}
			original := collectCIEvents(t, ctx, events)
			if len(original) != 2 || original[0].Type != executor.EventStarted || original[1].Type != tc.terminal || original[1].ExitCode != tc.code {
				t.Fatalf("unexpected terminal events: %+v", original)
			}
			if tc.name == "resource_usage" {
				usage := original[1].Usage
				if !usage.Valid() || usage.CPUTimeMillis == nil || usage.PeakMemoryBytes == nil || *usage.PeakMemoryBytes <= 0 {
					t.Fatalf("systemd resource sampling unavailable: %+v", usage)
				}
			}
			identity, err := os.ReadFile(filepath.Join(work, "identity"))
			if err != nil || strings.TrimSpace(string(identity)) != "yunling-runner" {
				t.Fatalf("workload identity: %s %v", identity, err)
			}
			if tc.code < 0 {
				assertCIChildGone(t, ctx, filepath.Join(work, "child.pid"))
			}
			// Remove the source to prove replay is not a new execution. Start a
			// separate test process, not merely a second Runner object.
			if err := os.Remove(script); err != nil {
				t.Fatal(err)
			}
			assignmentJSON, _ := json.Marshal(a)
			expectedJSON, _ := json.Marshal(original)
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSystemdReplayHelper$", "-test.count=1", "-test.v")
			command.Env = append(os.Environ(), "YUNLING_CI_ASSIGNMENT="+string(assignmentJSON), "YUNLING_CI_EXPECTED="+string(expectedJSON))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("fresh-process replay: %v\n%s", err, output)
			}
			launches, err := os.ReadFile(filepath.Join(work, "launches"))
			if err != nil || string(launches) != "start\n" {
				t.Fatalf("duplicate workload: %q %v", launches, err)
			}
		})
	}
}

func TestSystemdReplayHelper(t *testing.T) {
	requireSystemdCI(t)
	var a agentprotocol.Assignment
	var want []executor.Event
	if err := json.Unmarshal([]byte(os.Getenv("YUNLING_CI_ASSIGNMENT")), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(os.Getenv("YUNLING_CI_EXPECTED")), &want); err != nil {
		t.Fatal(err)
	}
	runner := executor.NewRunner(executor.NewSystemdLauncher(), time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := runner.Start(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if got := collectCIEvents(t, ctx, events); !reflect.DeepEqual(got, want) {
		t.Fatalf("replay mismatch: got=%+v want=%+v", got, want)
	}
	a.ExecutionToken = "conflicting-token"
	if _, err := runner.Start(ctx, a); !errors.Is(err, executor.ErrExecutionTokenMismatch) {
		t.Fatalf("conflicting replay accepted: %v", err)
	}
}

func collectCIEvents(t *testing.T, ctx context.Context, events <-chan executor.Event) []executor.Event {
	t.Helper()
	var result []executor.Event
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return result
			}
			result = append(result, event)
		case <-ctx.Done():
			t.Fatal("systemd event deadline exceeded")
			return nil
		}
	}
}

func awaitCIFile(t *testing.T, ctx context.Context, path string) []byte {
	t.Helper()
	for {
		body, err := os.ReadFile(path)
		if err == nil && len(body) > 0 {
			return body
		}
		select {
		case <-ctx.Done():
			t.Fatalf("fixture did not become ready: %s", path)
			return nil
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func assertCIChildGone(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	pid := strings.TrimSpace(string(awaitCIFile(t, ctx, path)))
	for {
		body, err := os.ReadFile("/proc/" + pid + "/stat")
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		// A zombie cannot execute further work and will be reaped by PID 1.
		if err == nil {
			if index := strings.LastIndex(string(body), ") "); index >= 0 && strings.HasPrefix(string(body)[index+2:], "Z ") {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("descendant %s survived cancellation/timeout", pid)
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
}
