package task_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/task"
)

func TestListRunEventsReplaysHistoricalAssignmentWithoutMessage(t *testing.T) {
	db := taskDatabase(t)
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	definition, err := service.Create(ctx, validTaskInput(scriptID, userID, "历史分配事件回放"))
	if err != nil {
		t.Fatal(err)
	}
	runs := task.NewRunService(db, nil, nil, taskClock)

	for _, test := range []struct {
		name, eventType, payload, wantMessage string
		state                                 task.RunState
	}{
		{"missing", "run.assigned", `{"serverId":"11111111-1111-4111-8111-111111111111","leaseId":"22222222-2222-4222-8222-222222222222"}`, "已分配执行服务器", task.Assigned},
		{"empty", "run.assigned", `{"message":""}`, "已分配执行服务器", task.Assigned},
		{"null", "run.assigned", `{"message":null}`, "已分配执行服务器", task.Assigned},
		{"whitespace", "run.assigned", `{"message":" \t\n　"}`, "已分配执行服务器", task.Assigned},
		{"custom", "run.assigned", `{"message":"  已分配到测试节点\n"}`, "  已分配到测试节点\n", task.Assigned},
		{"other-empty", "run.started", `{"message":""}`, "", task.Running},
		{"usage", "run.usage", `{}`, "", task.Running},
		{"unknown", "run.custom", `{"message":" \t"}`, " \t", task.Running},
	} {
		t.Run(test.name, func(t *testing.T) {
			run, err := service.Trigger(ctx, definition.ID, task.Trigger{Type: task.TriggerManual})
			if err != nil {
				t.Fatal(err)
			}
			assignedAt := taskClock().Add(time.Second)
			finishedAt := assignedAt.Add(time.Second)
			// Persist the historical payload directly: the reader must repair its display
			// without changing the stored event or using the run's current terminal state.
			if _, err := db.Exec(ctx, `
				INSERT INTO run_events (task_run_id, sequence, event_type, state, payload, occurred_at)
				VALUES ($1, 1, $2, $3, $4::jsonb, $5),
				       ($1, 2, 'run.succeeded', 'succeeded', '{"message":"任务已成功","exitCode":0}', $6)
			`, run.ID, test.eventType, test.state, test.payload, assignedAt, finishedAt); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `UPDATE task_runs SET state='succeeded', finished_at=$2 WHERE id=$1`, run.ID, finishedAt); err != nil {
				t.Fatal(err)
			}
			stream, err := runs.ListRunEvents(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(stream) != 3 {
				t.Fatalf("历史事件应完整回放：%+v", stream)
			}
			want := task.RunStreamEvent{
				ID: "state:00000000000000000001", Kind: "state", Sequence: 1,
				EventType: test.eventType, State: test.state, Message: test.wantMessage,
				OccurredAt: assignedAt,
			}
			assertRunEvent := func(got task.RunStreamEvent) {
				t.Helper()
				if !got.OccurredAt.Equal(want.OccurredAt) {
					t.Fatalf("历史事件时间不可改变：got=%s want=%s", got.OccurredAt, want.OccurredAt)
				}
				got.OccurredAt = want.OccurredAt
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("历史事件说明或元数据不正确：got=%+v want=%+v", got, want)
				}
			}
			assertRunEvent(stream[1])
			if stream[0].EventType != "run.queued" || stream[0].Message == "" ||
				stream[2].EventType != "run.succeeded" || stream[2].State != task.Succeeded ||
				stream[2].Message != "任务已成功" || stream[2].ExitCode == nil || *stream[2].ExitCode != 0 {
				t.Fatalf("相邻事件与终态退出码不可改变：%+v", stream)
			}

			request := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID+"/events?follow=false", nil)
			request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: userID, Roles: []auth.RoleName{auth.RoleViewer}}))
			recorder := httptest.NewRecorder()
			task.RunHandler(runs).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" {
				t.Fatalf("SSE 回放失败：%d %s", recorder.Code, recorder.Body.String())
			}
			frames := strings.Split(strings.TrimSuffix(recorder.Body.String(), "\n\n"), "\n\n")
			if len(frames) != 3 {
				t.Fatalf("SSE 应保留所有历史事件：%s", recorder.Body.String())
			}
			prefix := "id: " + want.ID + "\nevent: state\ndata: "
			if !strings.HasPrefix(frames[1], prefix) {
				t.Fatalf("SSE 事件 ID 和类型不可改变：%s", frames[1])
			}
			var replay task.RunStreamEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[1], prefix)), &replay); err != nil {
				t.Fatal(err)
			}
			assertRunEvent(replay)

			var unchanged bool
			if err := db.QueryRow(ctx, `SELECT payload=$2::jsonb FROM run_events WHERE task_run_id=$1 AND sequence=1`, run.ID, test.payload).Scan(&unchanged); err != nil || !unchanged {
				t.Fatalf("读取回放不能修改原始事件：unchanged=%v err=%v", unchanged, err)
			}
		})
	}
}
