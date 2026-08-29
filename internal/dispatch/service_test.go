package dispatch_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/dispatch"
	"yunling.local/platform/internal/secret"
)

func TestServiceDispatchesCompleteAssignment(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	store := &fakeDispatchStore{runs: []dispatch.Run{{
		ID: "run-1", ExecutionToken: "token-1", ServerID: "server-1",
		ScriptID: "script-1", ScriptVersionID: "version-1",
		Runtime: "bash", Entrypoint: "main.sh",
		Parameters:     map[string]any{"日期": "2026-08-29"},
		SecretBindings: map[string]string{"访问令牌": "secret-1"},
		Resources: agentprotocol.ResourceLimits{
			CPUMillicores: 100,
			MemoryBytes:   64 << 20,
			DiskBytes:     16 << 20,
		},
		Timeout: time.Minute,
	}}}
	sender := &fakeCommandSender{}
	resolver := &fakeSecretResolver{values: map[string]string{"secret-1": "不可输出的值"}}
	failures := &fakeFailureSink{}
	service := dispatch.NewService(store, sender, resolver, failures, func() time.Time { return now })

	if err := service.Dispatch(context.Background()); err != nil {
		t.Fatalf("派发运行：%v", err)
	}
	if sender.serverID != "server-1" || sender.calls != 1 {
		t.Fatalf("目标服务器错误：server=%q calls=%d", sender.serverID, sender.calls)
	}
	assignment := sender.command.Assignment
	if sender.command.Type != agentprotocol.CommandAssign || assignment == nil || assignment.RunID != "run-1" || assignment.ExecutionToken != "token-1" {
		t.Fatalf("执行身份不完整：%+v", sender.command)
	}
	if assignment.ScriptPath != "/var/lib/yunling-agent/script-cache/scripts/script-1/version-1/main.sh" {
		t.Fatalf("脚本路径错误：%s", assignment.ScriptPath)
	}
	if assignment.ScriptVersionID != "version-1" || assignment.Runtime != "bash" || assignment.Timeout != time.Minute {
		t.Fatalf("版本、运行时或超时不完整：%+v", assignment)
	}
	if assignment.Environment["YUNLING_RUN_ID"] != "run-1" || assignment.Environment["YUNLING_SCRIPT_VERSION_ID"] != "version-1" {
		t.Fatalf("执行环境身份不完整：%+v", assignment.Environment)
	}
	if assignment.Environment["YUNLING_PARAMETERS_JSON"] != `{"日期":"2026-08-29"}` {
		t.Fatalf("普通参数未下发：%+v", assignment.Environment)
	}
	if assignment.Environment["YUNLING_SECRETS_JSON"] != `{"访问令牌":"不可输出的值"}` {
		t.Fatal("敏感参数绑定未按名称下发")
	}
	if len(assignment.Arguments) != 0 || assignment.Resources.TasksMax != dispatch.DefaultTasksMax {
		t.Fatalf("参数或任务数上限错误：%+v", assignment)
	}
	if len(failures.events) != 0 || len(store.records) != 0 {
		t.Fatalf("成功派发不得记录失败：events=%+v records=%+v", failures.events, store.records)
	}
	if !store.cutoff.Equal(now.Add(-dispatch.DefaultRetryInterval)) || !store.now.Equal(now) || store.limit != dispatch.DefaultBatchSize {
		t.Fatalf("领取窗口错误：cutoff=%s now=%s limit=%d", store.cutoff, store.now, store.limit)
	}
}

func TestServiceKeepsAssignedRunForConnectionFailure(t *testing.T) {
	store := &fakeDispatchStore{runs: []dispatch.Run{{
		ID: "run-1", ExecutionToken: "token-1", ServerID: "server-1",
		ScriptID: "script-1", ScriptVersionID: "version-1", Runtime: "bash", Entrypoint: "main.sh",
	}}}
	sender := &fakeCommandSender{err: errors.New("拨号失败：secret-value")}
	failures := &fakeFailureSink{}
	service := dispatch.NewService(store, sender, nil, failures, time.Now)

	if err := service.Dispatch(context.Background()); err != nil {
		t.Fatalf("连接失败应保留运行等待重试：%v", err)
	}
	if len(store.records) != 1 || store.records[0].runID != "run-1" || store.records[0].token != "token-1" || store.records[0].message == "" {
		t.Fatalf("未记录可重试派发结果：%+v", store.records)
	}
	if strings.Contains(store.records[0].message, "secret-value") || len(failures.events) != 0 {
		t.Fatalf("连接错误不得泄密或推进失败终态：records=%+v events=%+v", store.records, failures.events)
	}
}

func TestServiceFailsRunWhenSecretCannotResolve(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 30, 0, 0, time.UTC)
	store := &fakeDispatchStore{runs: []dispatch.Run{{
		ID: "run-1", ExecutionToken: "token-1", ServerID: "server-1",
		ScriptID: "script-1", ScriptVersionID: "version-1", Runtime: "bash", Entrypoint: "main.sh",
		SecretBindings: map[string]string{"访问令牌": "secret-1"},
	}}}
	sender := &fakeCommandSender{}
	resolver := &fakeSecretResolver{err: errors.New("找不到 secret-1，其值可能是 不可输出的值")}
	failures := &fakeFailureSink{}
	service := dispatch.NewService(store, sender, resolver, failures, func() time.Time { return now })

	if err := service.Dispatch(context.Background()); err != nil {
		t.Fatalf("敏感参数失败已转成运行终态，不应阻塞扫描：%v", err)
	}
	if sender.calls != 0 || len(store.records) != 0 || len(failures.events) != 1 {
		t.Fatalf("永久失败处理错误：sender=%d records=%+v events=%+v", sender.calls, store.records, failures.events)
	}
	event := failures.events[0]
	if event.RunID != "run-1" || event.ExecutionToken != "token-1" || event.Sequence != 1 || event.Type != "failed" || event.ExitCode != -1 || !event.OccurredAt.Equal(now) {
		t.Fatalf("失败事件不完整：%+v", event)
	}
	if event.Message == "" || strings.Contains(event.Message, "secret-1") || strings.Contains(event.Message, "不可输出的值") {
		t.Fatalf("失败事件包含敏感信息：%q", event.Message)
	}
}

type dispatchRecord struct {
	runID   string
	token   string
	message string
}

type fakeDispatchStore struct {
	runs      []dispatch.Run
	claimErr  error
	recordErr error
	cutoff    time.Time
	now       time.Time
	limit     int
	records   []dispatchRecord
}

func (s *fakeDispatchStore) Claim(_ context.Context, cutoff, now time.Time, limit int) ([]dispatch.Run, error) {
	s.cutoff, s.now, s.limit = cutoff, now, limit
	return s.runs, s.claimErr
}

func (s *fakeDispatchStore) RecordResult(_ context.Context, runID, token, message string) error {
	s.records = append(s.records, dispatchRecord{runID: runID, token: token, message: message})
	return s.recordErr
}

type fakeCommandSender struct {
	serverID string
	command  agentprotocol.ExecutionCommand
	calls    int
	err      error
}

func (s *fakeCommandSender) SendExecutionCommand(_ context.Context, serverID string, command agentprotocol.ExecutionCommand) error {
	s.serverID, s.command = serverID, command
	s.calls++
	return s.err
}

type fakeSecretResolver struct {
	values map[string]string
	err    error
}

func (r *fakeSecretResolver) ResolveForRun(_ context.Context, refs []secret.ID) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	result := make(map[string]string, len(refs))
	for _, ref := range refs {
		result[string(ref)] = r.values[string(ref)]
	}
	return result, nil
}

type fakeFailureSink struct{ events []agentprotocol.RunEvent }

func (s *fakeFailureSink) Apply(_ context.Context, event agentprotocol.RunEvent) error {
	s.events = append(s.events, event)
	return nil
}
