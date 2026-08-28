package task

import "testing"

func TestRunStateTerminal(t *testing.T) {
	tests := []struct {
		name     string
		state    RunState
		terminal bool
	}{
		{name: "成功", state: Succeeded, terminal: true},
		{name: "失败", state: Failed, terminal: true},
		{name: "已超时", state: TimedOut, terminal: true},
		{name: "已取消", state: Cancelled, terminal: true},
		{name: "已过期", state: Expired, terminal: true},
		{name: "排队中", state: Queued, terminal: false},
		{name: "运行中", state: Running, terminal: false},
		{name: "状态待确认", state: Unknown, terminal: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.Terminal(); got != tt.terminal {
				t.Fatalf("Terminal() = %v，期望 %v", got, tt.terminal)
			}
		})
	}
}
