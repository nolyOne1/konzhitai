package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/dispatch"
)

func TestDispatchRequiresVerifiedScript(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       agentprotocol.SyncState
		verified    bool
		wantFailure string
		wantSend    bool
	}{
		{name: "missing", wantFailure: "尚未校验就绪"},
		{name: "pending", state: agentprotocol.SyncPending, wantFailure: "尚未校验就绪"},
		{name: "downloading", state: agentprotocol.SyncDownloading, wantFailure: "尚未校验就绪"},
		{name: "drifted", state: agentprotocol.SyncDrifted, wantFailure: "尚未校验就绪"},
		{name: "failed", state: agentprotocol.SyncFailed, wantFailure: "脚本同步失败"},
		{name: "checksum mismatch", state: agentprotocol.SyncReady, wantFailure: "校验值不一致"},
		{name: "ready", state: agentprotocol.SyncReady, verified: true, wantSend: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			store := &fakeDispatchStore{runs: []dispatch.Run{{
				ID: "run-1", ExecutionToken: "token-1", ServerID: "server-1",
				ScriptID: "script-1", ScriptVersionID: "version-1", Entrypoint: "main.sh",
				SyncState: tc.state, ScriptVerified: tc.verified,
			}}}
			sender, failures := &fakeCommandSender{}, &fakeFailureSink{}
			service := dispatch.NewService(store, sender, nil, failures, func() time.Time { return now })
			if err := service.Dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if (sender.calls == 1) != tc.wantSend {
				t.Fatalf("send calls=%d", sender.calls)
			}
			if tc.wantFailure != "" {
				if len(failures.events) != 1 {
					t.Fatalf("events=%+v", failures.events)
				}
				event := failures.events[0]
				if event.Type != "failed" || event.ExitCode != -1 || event.RunID != "run-1" || event.ExecutionToken != "token-1" || !strings.Contains(event.Message, tc.wantFailure) {
					t.Fatalf("failure=%+v", event)
				}
			} else if len(failures.events) != 0 {
				t.Fatalf("unexpected failure=%+v", failures.events)
			}
			if !tc.wantSend && tc.wantFailure == "" && len(store.records) != 1 {
				t.Fatal("missing waiting diagnostic")
			}
		})
	}
}

func TestDispatchNeverSendsUnverifiedVersion(t *testing.T) {
	store := &fakeDispatchStore{runs: []dispatch.Run{{ID: "run", ExecutionToken: "token", ServerID: "server", ScriptID: "script", ScriptVersionID: "pinned-version", Entrypoint: "main.sh", SyncState: agentprotocol.SyncPending}}}
	sender := &fakeCommandSender{}
	service := dispatch.NewService(store, sender, nil, &fakeFailureSink{}, time.Now)
	if err := service.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 0 {
		t.Fatal("dispatched before sync")
	}
	store.runs[0].SyncState, store.runs[0].ScriptVerified = agentprotocol.SyncReady, true
	if err := service.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 || sender.command.Assignment.ScriptVersionID != "pinned-version" {
		t.Fatalf("command=%+v", sender.command)
	}
}
