package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// WaitForNoActiveSystemdRuns allows systemd to finish stopping bound run units
// after an agent restart. A timeout or probe error must not authorize absence.
func WaitForNoActiveSystemdRuns(ctx context.Context) (bool, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		empty, err := NoActiveSystemdRuns(ctx)
		if err != nil || empty {
			return empty, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

// NoActiveSystemdRuns is only used before the new agent accepts assignments.
// An error, active run unit, or pending run job leaves the report non-authoritative.
func NoActiveSystemdRuns(ctx context.Context) (bool, error) {
	units, err := exec.CommandContext(ctx, systemctlPath, "list-units",
		"--no-legend", "--plain", "--full", "--no-pager", "yunling-run@*.service").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("检查隔离任务单元：%w", err)
	}
	if len(bytes.TrimSpace(units)) != 0 {
		return false, nil
	}
	jobs, err := exec.CommandContext(ctx, systemctlPath, "list-jobs",
		"--no-legend", "--plain", "--full", "--no-pager").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("检查隔离任务启动队列：%w", err)
	}
	return !bytes.Contains(jobs, []byte("yunling-run@")), nil
}
