package executor

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

func processUsage(process Process) *agentprotocol.ResourceUsage {
	if source, ok := process.(interface {
		Usage() *agentprotocol.ResourceUsage
	}); ok {
		return source.Usage()
	}
	return nil
}

func (p *systemdProcess) Usage() *agentprotocol.ResourceUsage {
	p.usageMu.RLock()
	defer p.usageMu.RUnlock()
	if p.usage == nil {
		return nil
	}
	value := *p.usage
	return &value
}

func (p *systemdProcess) sampleUsage() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, systemctlPath, "show", "--no-pager", "--property=CPUUsageNSec,MemoryCurrent,MemoryPeak,TasksCurrent", p.unitName)
	buffer := newBoundedBuffer(4096)
	command.Stdout = buffer
	if command.Run() != nil {
		return
	}
	usage := parseSystemdUsage(buffer.String(), time.Now().UTC())
	if usage == nil {
		return
	}
	p.usageMu.Lock()
	// Older systemd versions do not expose MemoryPeak. Preserve the largest
	// observed sample as an explicitly sampled peak on these hosts.
	if usage.PeakMemoryBytes == nil {
		usage.PeakMemoryBytes = usage.MemoryBytes
	}
	if p.usage != nil && p.usage.PeakMemoryBytes != nil && (usage.PeakMemoryBytes == nil || *p.usage.PeakMemoryBytes > *usage.PeakMemoryBytes) {
		usage.PeakMemoryBytes = p.usage.PeakMemoryBytes
	}
	if p.usage != nil && p.usage.CPUTimeMillis != nil && (usage.CPUTimeMillis == nil || *p.usage.CPUTimeMillis > *usage.CPUTimeMillis) {
		usage.CPUTimeMillis = p.usage.CPUTimeMillis
	}
	p.usage = usage
	p.usageMu.Unlock()
}

func parseSystemdUsage(output string, at time.Time) *agentprotocol.ResourceUsage {
	usage := &agentprotocol.ResourceUsage{SampledAt: at}
	for _, line := range strings.Split(output, "\n") {
		name, raw, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			continue
		}
		switch name {
		case "CPUUsageNSec":
			value /= 1_000_000
			usage.CPUTimeMillis = &value
		case "MemoryCurrent":
			usage.MemoryBytes = &value
		case "MemoryPeak":
			usage.PeakMemoryBytes = &value
		case "TasksCurrent":
			usage.Processes = &value
		}
	}
	if !usage.Valid() {
		return nil
	}
	return usage
}
