package executor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"yunling.local/platform/internal/executor"
)

type recordFailingLauncher struct{ starts int }

func (l *recordFailingLauncher) Start(context.Context, executor.LaunchSpec) (executor.Process, error) {
	l.starts++
	return nil, errors.New("fixture launch failure")
}

func TestExecutionRecordReplaysLaunchFailureWithoutStartingAgain(t *testing.T) {
	_, a := newTestRunner(t, newFakeLauncher(true), time.Second)
	root := filepath.Dir(filepath.Dir(a.ScriptPath))
	launcher := &recordFailingLauncher{}
	runner := executor.NewRunner(launcher, time.Second,
		executor.WithWorkRoot(filepath.Join(root, "runs")),
		executor.WithAllowedScriptRoots(filepath.Join(root, "cache")),
		executor.WithAllowedRuntimes("python3"))
	first, err := runner.Start(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	original := readRecordedEvents(t, first)
	if len(original) != 1 || original[0].Type != executor.EventFailed || original[0].Sequence != 1 {
		t.Fatalf("invalid start failure: %+v", original)
	}
	second, err := runner.Start(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if got := readRecordedEvents(t, second); !reflect.DeepEqual(original, got) || launcher.starts != 1 {
		t.Fatalf("start failure not replayed exactly: %+v starts=%d", got, launcher.starts)
	}
}

func readRecordedEvents(t *testing.T, events <-chan executor.Event) []executor.Event {
	t.Helper()
	var result []executor.Event
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return result
			}
			result = append(result, event)
		case <-time.After(3 * time.Second):
			t.Fatal("events did not finish")
		}
	}
}

func TestExecutionRecordAllowsOnlyOneRunnerToClaim(t *testing.T) {
	firstLauncher := newFakeLauncher(true)
	first, a := newTestRunner(t, firstLauncher, time.Second)
	secondLauncher := newFakeLauncher(true)
	root := filepath.Dir(filepath.Dir(a.ScriptPath))
	second := executor.NewRunner(secondLauncher, time.Second,
		executor.WithWorkRoot(filepath.Join(root, "runs")),
		executor.WithAllowedScriptRoots(filepath.Join(root, "cache")),
		executor.WithAllowedRuntimes("python3"))
	start := make(chan struct{})
	var wg sync.WaitGroup
	type result struct {
		events <-chan executor.Event
		err    error
	}
	results := make(chan result, 2)
	for _, runner := range []*executor.Runner{first, second} {
		wg.Add(1)
		go func(runner *executor.Runner) {
			defer wg.Done()
			<-start
			events, err := runner.Start(context.Background(), a)
			results <- result{events, err}
		}(runner)
	}
	close(start)
	wg.Wait()
	close(results)
	_ = firstLauncher.process.Terminate()
	_ = secondLauncher.process.Terminate()
	for result := range results {
		if result.err == nil {
			_ = readRecordedEvents(t, result.events)
		} else if !errors.Is(result.err, executor.ErrExecutionUncertain) {
			t.Fatal(result.err)
		}
	}
	if got := firstLauncher.starts + secondLauncher.starts; got != 1 {
		t.Fatalf("two runners launched %d processes", got)
	}
}

func TestExecutionRecordStorageFailurePreventsStart(t *testing.T) {
	launcher := newFakeLauncher(true)
	_, a := newTestRunner(t, launcher, time.Second)
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := executor.NewRunner(launcher, time.Second, executor.WithWorkRoot(blockedRoot), executor.WithAllowedRuntimes("python3"))
	if _, err := runner.Start(context.Background(), a); !errors.Is(err, executor.ErrExecutionUncertain) {
		t.Fatalf("storage failure did not fail closed: %v", err)
	}
	if launcher.starts != 0 {
		t.Fatal("launched without durable record")
	}
}

func TestExecutionRecordReplaysExactResultsAfterRunnerRestart(t *testing.T) {
	launcher := newFakeLauncher(true)
	runner, a := newTestRunner(t, launcher, time.Second)
	events, err := runner.Start(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err := launcher.process.Terminate(); err != nil {
		t.Fatal(err)
	}
	original := readRecordedEvents(t, events)
	workRoot := filepath.Join(filepath.Dir(filepath.Dir(a.ScriptPath)), "runs")
	secondLauncher := newFakeLauncher(true)
	restarted := executor.NewRunner(secondLauncher, time.Second, executor.WithWorkRoot(workRoot), executor.WithAllowedRuntimes("python3"))
	// Replaying must not depend on the continued presence of the script cache.
	if err := os.Remove(a.ScriptPath); err != nil {
		t.Fatal(err)
	}
	replay, err := restarted.Start(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if got := readRecordedEvents(t, replay); !reflect.DeepEqual(got, original) {
		t.Fatalf("replay changed event identity/content: got=%+v want=%+v", got, original)
	}
	if secondLauncher.starts != 0 {
		t.Fatal("restart replay launched a process")
	}
	a.ExecutionToken = "other-token"
	if _, err := restarted.Start(context.Background(), a); !errors.Is(err, executor.ErrExecutionTokenMismatch) {
		t.Fatalf("conflicting token accepted: %v", err)
	}
}

func TestExecutionRecordIncompleteAndCorruptRecordsFailClosed(t *testing.T) {
	launcher := newFakeLauncher(true)
	runner, a := newTestRunner(t, launcher, time.Second)
	events, err := runner.Start(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launcher.process.Terminate(); _ = readRecordedEvents(t, events) })
	workRoot := filepath.Join(filepath.Dir(filepath.Dir(a.ScriptPath)), "runs")
	otherLauncher := newFakeLauncher(true)
	restarted := executor.NewRunner(otherLauncher, time.Second, executor.WithWorkRoot(workRoot), executor.WithAllowedRuntimes("python3"))
	if _, err := restarted.Start(context.Background(), a); !errors.Is(err, executor.ErrExecutionUncertain) {
		t.Fatalf("incomplete claim accepted: %v", err)
	}
	if otherLauncher.starts != 0 {
		t.Fatal("incomplete record allowed a launch")
	}
	if err := launcher.process.Terminate(); err != nil {
		t.Fatal(err)
	}
	_ = readRecordedEvents(t, events)
	path := filepath.Join(workRoot, ".execution-records", a.RunID+".result.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Start(context.Background(), a); !errors.Is(err, executor.ErrExecutionUncertain) {
		t.Fatalf("corrupt result accepted: %v", err)
	}
	if otherLauncher.starts != 0 {
		t.Fatal("corrupt result allowed a launch")
	}
}
