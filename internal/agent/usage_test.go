package agent

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

type usageReportSender struct {
	reports chan agentprotocol.RunningReport
}

func (s usageReportSender) SendHeartbeat(context.Context, agentprotocol.Heartbeat) error { return nil }
func (s usageReportSender) SendRunningReport(_ context.Context, r agentprotocol.RunningReport) error {
	s.reports <- r
	return nil
}

func TestHeartbeatReportsUsageWithoutAuthorizingProcessAbsence(t *testing.T) {
	ticker := &fakeTicker{values: make(chan time.Time, 1), ready: make(chan time.Duration, 1)}
	sender := usageReportSender{reports: make(chan agentprotocol.RunningReport, 1)}
	memory := int64(1024)
	at := time.Now().UTC()
	usage := &agentprotocol.ResourceUsage{MemoryBytes: &memory, SampledAt: at}
	client := NewClient("server-1", "test", NewCollector(fakeStats{snapshot: Stats{CPUTotalMilli: 1000, MemoryTotalBytes: 1024, DiskTotalBytes: 1024, DiskFreeBytes: 1024}}, []string{"bash"}), sender,
		WithTickerFactory(func(d time.Duration) Ticker { ticker.ready <- d; return ticker }),
		WithRunningProcesses(func() []agentprotocol.RunningProcess {
			return []agentprotocol.RunningProcess{{RunID: "run-1", ExecutionToken: "token", Usage: usage}}
		}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	<-ticker.ready
	ticker.values <- at
	select {
	case report := <-sender.reports:
		if report.Authoritative || report.ServerID != "server-1" || len(report.Processes) != 1 || !report.Processes[0].Usage.Equal(usage) {
			t.Fatalf("report: %+v", report)
		}
	case <-time.After(time.Second):
		t.Fatal("no usage report")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
