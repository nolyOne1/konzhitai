package task

type RunState string

const (
	Queued     RunState = "queued"
	Scheduling RunState = "scheduling"
	Assigned   RunState = "assigned"
	Syncing    RunState = "syncing"
	Running    RunState = "running"
	Succeeded  RunState = "succeeded"
	Failed     RunState = "failed"
	TimedOut   RunState = "timed_out"
	Cancelled  RunState = "cancelled"
	Expired    RunState = "expired"
	Unknown    RunState = "unknown"
)

func (s RunState) Terminal() bool {
	switch s {
	case Succeeded, Failed, TimedOut, Cancelled, Expired:
		return true
	default:
		return false
	}
}
