package executor

import (
	"testing"
	"time"
)

func TestSystemdUsageDistinguishesUnavailableFromZero(t *testing.T) {
	at := time.Now().UTC()
	usage := parseSystemdUsage("CPUUsageNSec=2500000000\nMemoryCurrent=0\nMemoryPeak=18446744073709551615\nTasksCurrent=3\n", at)
	if usage == nil || *usage.CPUTimeMillis != 2500 || usage.MemoryBytes == nil || *usage.MemoryBytes != 0 || usage.PeakMemoryBytes != nil || *usage.Processes != 3 || !usage.SampledAt.Equal(at) {
		t.Fatalf("usage: %+v", usage)
	}
	if parseSystemdUsage("MemoryCurrent=[not set]\nCPUUsageNSec=-1\nTasksCurrent=infinity", at) != nil {
		t.Fatal("unavailable counters must not become zero measurements")
	}
}
