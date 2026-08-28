package task_test

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/task"
)

func TestScheduleDueDoesNotDuplicateSameFireTime(t *testing.T) {
	db := taskDatabase(t)
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	definition, err := service.Create(ctx, validTaskInput(scriptID, userID, "凌晨归档"))
	if err != nil {
		t.Fatalf("创建任务定义：%v", err)
	}
	schedule, err := service.CreateSchedule(ctx, task.ScheduleInput{
		DefinitionID:   definition.ID,
		CronExpression: "0 2 * * *",
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("创建定时计划：%v", err)
	}
	if schedule.Timezone != "Asia/Shanghai" {
		t.Fatalf("未指定时区时应使用 Asia/Shanghai，实际为 %q", schedule.Timezone)
	}

	first, err := service.ScheduleDue(ctx, mustTaskTime(t, "2026-08-28T02:00:00+08:00"))
	if err != nil {
		t.Fatalf("首次触发计划：%v", err)
	}
	second, err := service.ScheduleDue(ctx, mustTaskTime(t, "2026-08-28T02:00:10+08:00"))
	if err != nil {
		t.Fatalf("重复扫描计划：%v", err)
	}
	if len(first) != 1 || len(second) != 0 {
		t.Fatalf("同一触发时刻只能产生一个运行实例：first=%d second=%d", len(first), len(second))
	}
	if first[0].ScheduledFor == nil || !first[0].ScheduledFor.Equal(mustTaskTime(t, "2026-08-28T02:00:00+08:00")) {
		t.Fatalf("运行实例应保留计划触发时刻：%v", first[0].ScheduledFor)
	}
}

func TestValidateCronRejectsInvalidExpressionAndTimezone(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		timezone   string
	}{
		{name: "字段数量错误", expression: "0 2 * *", timezone: "Asia/Shanghai"},
		{name: "分钟超出范围", expression: "60 2 * * *", timezone: "Asia/Shanghai"},
		{name: "时区不存在", expression: "0 2 * * *", timezone: "Mars/Base"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := task.ValidateCron(test.expression, test.timezone); err == nil {
				t.Fatal("无效的 Cron 计划必须被拒绝")
			}
		})
	}
	if err := task.ValidateCron("*/15 9-18 * * 1-5", "Asia/Shanghai"); err != nil {
		t.Fatalf("常用五段 Cron 规则应被接受：%v", err)
	}
}

func mustTaskTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("解析测试时间：%v", err)
	}
	return parsed
}
