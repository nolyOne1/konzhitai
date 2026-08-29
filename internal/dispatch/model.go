package dispatch

import (
	"context"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

type Run struct {
	ID              string
	ExecutionToken  string
	ServerID        string
	ScriptID        string
	ScriptVersionID string
	Runtime         string
	Entrypoint      string
	Parameters      map[string]any
	SecretBindings  map[string]string
	Resources       agentprotocol.ResourceLimits
	Timeout         time.Duration
	Attempt         int
}

type Store interface {
	Claim(ctx context.Context, cutoff, now time.Time, limit int) ([]Run, error)
	RecordResult(ctx context.Context, runID, executionToken, dispatchError string) error
}
