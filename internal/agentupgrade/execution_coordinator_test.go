package agentupgrade

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

func TestCoordinatorRejectsChangedSourceBeforeFirstInstall(t *testing.T) {
	for _, test := range []struct{ name, source, target, current string }{
		{"ordinary upgrade drifted", "0.2.4", "0.2.6", "0.2.10"},
		{"historical rollback drifted", "0.2.6", "0.2.4", "0.2.10"},
		{"already target before dispatch", "0.2.4", "0.2.6", "0.2.6"},
		{"changed source identifier", "0.2.4", "0.2.6", "v0.2.4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := executionCoordinatorFixture(TargetDraining, test.source, test.target, test.current)
			store.plan.Targets = append(store.plan.Targets, Target{ID: "peer", ServerID: "s2", BatchNumber: 1, Status: TargetWaiting})
			sender := &fakeUpgradeSender{}
			coordinator := NewCoordinator(store, sender, fixedCoordinatorNow)
			for range 2 {
				if err := coordinator.Scan(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			target := store.plan.Targets[0]
			if len(sender.commands) != 0 || store.plan.Status != PlanPaused || target.Status != TargetManualIntervention ||
				target.ErrorCode != "source_version_changed" || target.SourceVersion != test.source || target.TargetVersion != test.target {
				t.Fatalf("source drift must pause before any install command: plan=%+v commands=%+v", store.plan, sender.commands)
			}
			if store.plan.Targets[1].Status != TargetWaiting {
				t.Fatalf("a source mismatch must stop starting other waiting targets: %+v", store.plan.Targets[1])
			}
		})
	}
}

func TestCoordinatorBindsReplayedInstallToSourceOrInstalledTarget(t *testing.T) {
	for _, status := range []TargetStatus{TargetDownloading, TargetVerifying, TargetInstalling} {
		for _, test := range []struct{ name, source, target, current string }{
			{"original source", "0.2.4", "0.2.6", "0.2.4"},
			{"installed target", "0.2.4", "0.2.6", "0.2.6"},
			{"unrelated newer version", "0.2.4", "0.2.6", "0.2.10"},
			{"rollback original source", "0.2.6", "0.2.4", "0.2.6"},
			{"rollback installed target", "0.2.6", "0.2.4", "0.2.4"},
			{"rollback drifted source", "0.2.6", "0.2.4", "0.2.10"},
		} {
			t.Run(string(status)+"/"+test.name, func(t *testing.T) {
				store := executionCoordinatorFixture(status, test.source, test.target, test.current)
				sender := &fakeUpgradeSender{}
				if err := NewCoordinator(store, sender, fixedCoordinatorNow).Scan(context.Background()); err != nil {
					t.Fatal(err)
				}
				if test.current != test.source && test.current != test.target {
					if len(sender.commands) != 0 || store.plan.Status != PlanPaused || store.plan.Targets[0].Status != TargetManualIntervention {
						t.Fatalf("restart must not resend to a different version: plan=%+v commands=%+v", store.plan, sender.commands)
					}
					return
				}
				if len(sender.commands) != 1 || sender.commands[0].CommandID != "command-1" || sender.commands[0].Action != agentprotocol.UpgradeInstall ||
					sender.commands[0].SourceVersion != test.source || sender.commands[0].TargetVersion != test.target || store.plan.Targets[0].Status != status {
					t.Fatalf("known in-flight transition must retain its idempotent command: plan=%+v commands=%+v", store.plan, sender.commands)
				}
			})
		}
	}
}

func TestCoordinatorSourceBindingAllowsInstallReconnectAndHealthCheck(t *testing.T) {
	for _, transition := range []struct{ name, source, target string }{
		{"upgrade", "0.2.4", "0.2.6"},
		{"explicit rollback", "0.2.6", "0.2.4"},
	} {
		t.Run(transition.name, func(t *testing.T) {
			store := executionCoordinatorFixture(TargetDraining, transition.source, transition.target, transition.source)
			sender := &fakeUpgradeSender{}
			now := fixedCoordinatorNow()
			coordinator := NewCoordinator(store, sender, func() time.Time { return now })
			if err := coordinator.Scan(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(sender.commands) != 1 || store.plan.Targets[0].Status != TargetDownloading {
				t.Fatalf("bound source must begin installation: %+v", store.plan)
			}
			if err := coordinator.ApplyUpgradeEvent(context.Background(), "s1", agentprotocol.UpgradeEvent{
				TargetID: "target-canary", CommandID: "command-1", Stage: agentprotocol.StageReconnecting,
			}); err != nil {
				t.Fatal(err)
			}
			if err := coordinator.ObserveHeartbeat(context.Background(), agentprotocol.Heartbeat{
				ServerID: "s1", AgentVersion: transition.target, Upgrade: &agentprotocol.UpgradeRuntimeState{TargetID: "target-canary", CommandID: "command-1"},
			}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(31 * time.Second)
			runtime := store.runtime["s1"]
			runtime.AgentVersion, runtime.LastSeenAt = transition.target, &now
			store.runtime["s1"] = runtime
			if err := coordinator.Scan(context.Background()); err != nil {
				t.Fatal(err)
			}
			if store.plan.Status != PlanSucceeded || store.plan.Targets[0].Status != TargetSucceeded || len(sender.commands) != 1 {
				t.Fatalf("target reconnect must complete without spurious rollback: plan=%+v commands=%+v", store.plan, sender.commands)
			}
		})
	}
}

func TestCoordinatorSourceBindingKeepsAutomaticRollbackRecovery(t *testing.T) {
	store := executionCoordinatorFixture(TargetRollingBack, "0.2.4", "0.2.6", "0.2.6")
	store.plan.Status = PlanPaused
	store.plan.Targets[0].CommandID = "rollback-command"
	store.plan.Targets[0].InstallCommandID = "command-1"
	sender := &fakeUpgradeSender{}
	coordinator := NewCoordinator(store, sender, fixedCoordinatorNow)
	if err := coordinator.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.commands) != 1 || sender.commands[0].Action != agentprotocol.UpgradeRollback || sender.commands[0].TargetVersion != "0.2.4" {
		t.Fatalf("automatic rollback must retain its separate recovery command: %+v", sender.commands)
	}
	if err := coordinator.ObserveHeartbeat(context.Background(), agentprotocol.Heartbeat{
		ServerID: "s1", AgentVersion: "0.2.4", Upgrade: &agentprotocol.UpgradeRuntimeState{TargetID: "target-canary", CommandID: "rollback-command"},
	}); err != nil {
		t.Fatal(err)
	}
	if store.plan.Targets[0].Status != TargetRolledBack {
		t.Fatalf("original source reconnect must complete rollback: %+v", store.plan.Targets[0])
	}
}

func executionCoordinatorFixture(status TargetStatus, source, target, current string) *coordinatorMemory {
	store := coordinatorFixture()
	store.plan.TargetVersion, store.release.Version = target, target
	store.plan.Targets[0].Status = status
	store.plan.Targets[0].SourceVersion, store.plan.Targets[0].TargetVersion = source, target
	runtime := store.runtime["s1"]
	runtime.AgentVersion = current
	store.runtime["s1"] = runtime
	return store
}
