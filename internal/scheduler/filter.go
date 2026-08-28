package scheduler

import (
	"strings"

	"yunling.local/platform/internal/server"
	"yunling.local/platform/internal/task"
)

func Filter(run task.Run, servers []server.Snapshot) []Candidate {
	candidates := make([]Candidate, 0, len(servers))
	for _, item := range servers {
		if item.Status != server.StatusOnline || !item.Enabled || item.Draining {
			continue
		}
		if !labelsMatch(run.RequiredLabels, item.Labels) {
			continue
		}
		if !runtimeAvailable(run.RequiredRuntime, item.Runtimes) {
			continue
		}
		if item.MaxConcurrency <= 0 || item.RunningTasks >= item.MaxConcurrency {
			continue
		}
		if item.BlockedScriptVersions[run.ScriptVersionID] {
			continue
		}
		if item.CPUAvailableMillicores < run.Resources.CPUMillicores {
			continue
		}
		if item.MemoryAvailableBytes < run.Resources.MemoryBytes {
			continue
		}
		if item.DiskAvailableBytes < run.Resources.DiskBytes {
			continue
		}
		weight := item.SchedulingWeight
		if weight <= 0 {
			weight = 100
		}
		candidates = append(candidates, Candidate{
			ServerID: item.ID,
			Total: task.Resources{
				CPUMillicores: item.CPUTotalMillicores,
				MemoryBytes:   item.MemoryTotalBytes,
				DiskBytes:     item.DiskTotalBytes,
			},
			Available: task.Resources{
				CPUMillicores: item.CPUAvailableMillicores,
				MemoryBytes:   item.MemoryAvailableBytes,
				DiskBytes:     item.DiskAvailableBytes,
			},
			RunningTasks: item.RunningTasks, MaxConcurrency: item.MaxConcurrency,
			ScriptCached:  item.ReadyScriptVersions[run.ScriptVersionID],
			FairnessScore: item.FairnessScore, SchedulingWeight: weight,
		})
	}
	return candidates
}

func labelsMatch(required, available map[string]string) bool {
	for key, value := range required {
		if available[key] != value {
			return false
		}
	}
	return true
}

func runtimeAvailable(required string, runtimes []string) bool {
	for _, runtimeName := range runtimes {
		if strings.EqualFold(strings.TrimSpace(runtimeName), strings.TrimSpace(required)) {
			return true
		}
	}
	return false
}
