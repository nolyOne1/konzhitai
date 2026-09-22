package executor

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"yunling.local/platform/internal/agentprotocol"
)

// An incomplete record is not evidence that a process never started. Keep the
// run uncertain rather than report a fabricated failure or launch it again.
var ErrExecutionUncertain = errors.New("执行记录未完成或不可读，需对账确认，拒绝重复启动")

type executionRecord struct {
	RunID     string  `json:"run_id"`
	TokenHash string  `json:"token_hash"`
	Events    []Event `json:"events"`
}

func recordIdentity(a agentprotocol.Assignment) executionRecord {
	return executionRecord{RunID: a.RunID, TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(a.ExecutionToken)))}
}

func (r *Runner) recordBase(runID string) (string, error) {
	// The private directory is outside each script's writable run directory.
	root, err := filepath.Abs(r.workRoot)
	if err != nil {
		return "", err
	}
	for _, dir := range []string{root, filepath.Join(root, ".execution-records")} {
		mode := os.FileMode(0o700)
		if dir == root {
			mode = 0o750
		}
		if err := os.MkdirAll(dir, mode); err != nil {
			return "", err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrExecutionUncertain
		}
		if dir != root && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return "", ErrExecutionUncertain
		}
	}
	return filepath.Join(root, ".execution-records", runID), nil
}

func readExecutionRecord(path string) (executionRecord, error) {
	var record executionRecord
	info, err := os.Lstat(path)
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return record, ErrExecutionUncertain
	}
	f, err := os.Open(path)
	if err != nil {
		return record, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return record, err
	}
	err = json.Unmarshal(data, &record)
	return record, err
}

// Records are immutable. Exclusive creation also arbitrates two Runner objects
// (or agents) using the same work root. A torn record remains fail-closed.
func writeExecutionRecord(path string, record executionRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		err = dir.Sync()
		return errors.Join(err, dir.Close())
	}
	return nil
}

func (r *Runner) replay(a agentprotocol.Assignment) (<-chan Event, bool, error) {
	base, err := r.recordBase(a.RunID)
	if err != nil {
		return nil, true, fmt.Errorf("%w: %v", ErrExecutionUncertain, err)
	}
	claim, err := readExecutionRecord(base + ".claim.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || claim.RunID != a.RunID {
		return nil, true, ErrExecutionUncertain
	}
	if claim.TokenHash != recordIdentity(a).TokenHash {
		return nil, true, ErrExecutionTokenMismatch
	}
	result, err := readExecutionRecord(base + ".result.json")
	if err != nil || result.RunID != claim.RunID || result.TokenHash != claim.TokenHash || len(result.Events) < 1 || len(result.Events) > 2 {
		return nil, true, ErrExecutionUncertain
	}
	for i, event := range result.Events {
		if event.Sequence != uint64(i+1) || event.OccurredAt.IsZero() {
			return nil, true, ErrExecutionUncertain
		}
	}
	if len(result.Events) == 2 && result.Events[0].Type != EventStarted {
		return nil, true, ErrExecutionUncertain
	}
	switch result.Events[len(result.Events)-1].Type {
	case EventSucceeded, EventFailed, EventTimedOut, EventCancelled:
	default:
		return nil, true, ErrExecutionUncertain
	}
	return recordedEvents(result.Events), true, nil
}

func recordedEvents(events []Event) <-chan Event {
	output := make(chan Event, len(events))
	for _, event := range events {
		output <- event
	}
	close(output)
	return output
}

func (r *Runner) claimExecution(a agentprotocol.Assignment) error {
	base, err := r.recordBase(a.RunID)
	if err == nil {
		err = writeExecutionRecord(base+".claim.json", recordIdentity(a))
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrExecutionUncertain, err)
	}
	return nil
}

func (r *Runner) saveExecution(a agentprotocol.Assignment, events ...Event) error {
	base, err := r.recordBase(a.RunID)
	if err != nil {
		return err
	}
	record := recordIdentity(a)
	record.Events = events
	return writeExecutionRecord(base+".result.json", record)
}
