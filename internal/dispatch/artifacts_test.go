package dispatch_test

import (
	"context"
	"strings"
	"testing"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/dispatch"
)

func TestDispatchRequiresArtifactCapabilityAndPreservesPolicy(t *testing.T) {
	for _, supported := range []bool{false, true} {
		policy := &agentprotocol.ArtifactPolicy{AllowedGlobs: []string{"*.csv"}, MaxFileBytes: 1024, MaxTotalBytes: 2048}
		store := &fakeDispatchStore{runs: []dispatch.Run{{ID: "run", ServerID: "server", ExecutionToken: "token", ScriptID: "script", ScriptVersionID: "version", Runtime: "bash", Entrypoint: "main.sh", SyncState: agentprotocol.SyncReady, ScriptVerified: true, Artifacts: policy, ArtifactsSupported: supported}}}
		sender, failures := &fakeCommandSender{}, &fakeFailureSink{}
		if err := dispatch.NewService(store, sender, nil, failures, nil).Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if supported {
			if sender.calls != 1 || sender.command.Assignment.Artifacts != policy {
				t.Fatalf("capable agent did not receive policy: %+v", sender.command)
			}
		} else if sender.calls != 0 || len(failures.events) != 1 || !strings.Contains(failures.events[0].Message, "升级代理") {
			t.Fatalf("old agent accepted artifacts: sent=%d failures=%+v", sender.calls, failures.events)
		}
	}
}
