package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

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
