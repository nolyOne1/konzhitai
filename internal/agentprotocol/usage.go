package agentprotocol

import "time"

// Nil counters mean that the host cannot report the measurement.
type ResourceUsage struct {
	CPUTimeMillis   *int64    `json:"cpuTimeMillis,omitempty"`
	MemoryBytes     *int64    `json:"memoryBytes,omitempty"`
	PeakMemoryBytes *int64    `json:"peakMemoryBytes,omitempty"`
	Processes       *int64    `json:"processes,omitempty"`
	SampledAt       time.Time `json:"sampledAt"`
}

func (u *ResourceUsage) Valid() bool {
	if u == nil || u.SampledAt.IsZero() {
		return false
	}
	found := false
	for _, value := range []*int64{u.CPUTimeMillis, u.MemoryBytes, u.PeakMemoryBytes, u.Processes} {
		if value != nil {
			if *value < 0 {
				return false
			}
			found = true
		}
	}
	return found
}

func (u *ResourceUsage) Equal(other *ResourceUsage) bool {
	if u == nil || other == nil {
		return u == nil && other == nil
	}
	if !u.SampledAt.Equal(other.SampledAt) {
		return false
	}
	for index, value := range []*int64{u.CPUTimeMillis, u.MemoryBytes, u.PeakMemoryBytes, u.Processes} {
		right := []*int64{other.CPUTimeMillis, other.MemoryBytes, other.PeakMemoryBytes, other.Processes}[index]
		if value == nil || right == nil {
			if value != right {
				return false
			}
		} else if *value != *right {
			return false
		}
	}
	return true
}
