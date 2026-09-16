package executor

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExitEventIdentifiesActualWorkloadCode(t *testing.T) {
	runner := &Runner{now: time.Now}
	for _, tc := range []struct {
		code int
		err  error
		want string
	}{
		{7, errors.New("systemctl 控制进程错误：exit status 1"), "脚本退出码 7"},
		{42, nil, "脚本退出码 42"},
		{-1, errors.New("未取得任务真实退出码"), "未取得任务真实退出码"},
		{0, errors.New("读取日志失败"), "读取日志失败"},
	} {
		event := runner.exitEvent(2, processResult{exitCode: tc.code, err: tc.err})
		if event.Type != EventFailed || event.ExitCode != tc.code || !strings.Contains(event.Message, tc.want) {
			t.Fatalf("event=%+v", event)
		}
		if tc.err != nil && !strings.Contains(event.Message, tc.err.Error()) {
			t.Fatal("diagnostic lost")
		}
		if tc.code < 0 && strings.Contains(event.Message, "脚本退出码") {
			t.Fatal("unknown result claimed a normal exit")
		}
	}
	if event := runner.exitEvent(2, processResult{}); event.Type != EventSucceeded {
		t.Fatalf("event=%+v", event)
	}
}
