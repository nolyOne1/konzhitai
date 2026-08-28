package scheduler

import (
	"sort"

	"yunling.local/platform/internal/task"
)

const scoreBasis int64 = 10000

func Score(_ task.Run, candidate Candidate) int64 {
	memory := ratio(candidate.Available.MemoryBytes, candidate.Total.MemoryBytes)
	cpu := ratio(int64(candidate.Available.CPUMillicores), int64(candidate.Total.CPUMillicores))
	availableSlots := candidate.MaxConcurrency - candidate.RunningTasks
	concurrency := ratio(int64(availableSlots), int64(candidate.MaxConcurrency))
	cache := int64(0)
	if candidate.ScriptCached {
		cache = scoreBasis
	}
	fairness := clamp(candidate.FairnessScore, 0, scoreBasis)
	base := (memory*35 + cpu*25 + concurrency*20 + cache*15 + fairness*5) / 100
	weight := candidate.SchedulingWeight
	if weight <= 0 {
		weight = 100
	}
	return base * int64(weight) / 100
}

func RankCandidates(run task.Run, candidates []Candidate) []Candidate {
	ranked := append([]Candidate(nil), candidates...)
	sort.SliceStable(ranked, func(left, right int) bool {
		leftScore, rightScore := Score(run, ranked[left]), Score(run, ranked[right])
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return ranked[left].ServerID < ranked[right].ServerID
	})
	return ranked
}

func ratio(available, total int64) int64 {
	if total <= 0 || available <= 0 {
		return 0
	}
	return clamp(available*scoreBasis/total, 0, scoreBasis)
}

func clamp(value, minimum, maximum int64) int64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
