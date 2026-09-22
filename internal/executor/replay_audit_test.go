package executor_test

import (
	"context"
	"testing"
	"time"
)

// A delayed redelivery must not restart a run after the active entry is removed.
// The fake launcher records starts; it never executes an actual script.
func TestRetryAuditCompletedAssignmentCannotRestart(t *testing.T) {
	launcher := newFakeLauncher(true)
	runner, assignment := newTestRunner(t, launcher, time.Second)
	events, err := runner.Start(context.Background(), assignment)
	if err != nil {
		t.Fatal(err)
	}
	if err := launcher.process.Terminate(); err != nil {
		t.Fatal(err)
	}
	_ = collectEventTypes(t, events)
	if runner.RunningCount() != 0 {
		t.Fatal("fixture has not finished")
	}
	duplicateEvents, err := runner.Start(context.Background(), assignment)
	if err == nil {
		_ = collectEventTypes(t, duplicateEvents)
	}
	if launcher.starts != 1 {
		t.Fatalf("completed run/token redelivery launched %d processes; want 1", launcher.starts)
	}
}
