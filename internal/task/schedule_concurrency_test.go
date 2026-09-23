package task_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/task"
)

func TestScheduleDueRechecksPlansChangedAfterScan(t *testing.T) {
	db := taskDatabase(t)
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	fireTime := mustTaskTime(t, "2026-08-28T02:00:00+08:00")

	for _, change := range []string{"disable", "edit-with-same-fire-time", "delete"} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			definition, err := service.Create(ctx, validTaskInput(scriptID, userID, change))
			if err != nil {
				t.Fatal(err)
			}
			schedule, err := service.CreateSchedule(ctx, task.ScheduleInput{DefinitionID: definition.ID, CronExpression: "0 2 * * *", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			defer service.DeleteSchedule(context.Background(), definition.ID, schedule.ID)
			otherDefinition, err := service.Create(ctx, validTaskInput(scriptID, userID, "另一到期任务-"+change))
			if err != nil {
				t.Fatal(err)
			}
			otherSchedule, err := service.CreateSchedule(ctx, task.ScheduleInput{DefinitionID: otherDefinition.ID, CronExpression: "1 2 * * *", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			defer service.DeleteSchedule(context.Background(), otherDefinition.ID, otherSchedule.ID)

			blocker, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(ctx, `SELECT id FROM task_definitions WHERE id=$1 FOR UPDATE`, definition.ID); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				runs []task.Run
				err  error
			}
			finished := make(chan outcome, 1)
			go func() {
				runs, err := service.ScheduleDue(ctx, fireTime.Add(time.Minute))
				finished <- outcome{runs, err}
			}()
			// The worker can read the plan, but it cannot yet lock the task. This
			// establishes the stale scan deterministically without a timing sleep.
			waitForScheduleDefinitionLock(t, ctx, db, blocker.Conn().PgConn().PID())
			switch change {
			case "disable":
				_, err = service.UpdateSchedule(ctx, schedule.ID, task.ScheduleInput{DefinitionID: definition.ID, CronExpression: "0 2 * * *", Enabled: false})
			case "edit-with-same-fire-time":
				var updated task.Schedule
				updated, err = service.UpdateSchedule(ctx, schedule.ID, task.ScheduleInput{DefinitionID: definition.ID, CronExpression: "0 2 * * 5", Enabled: true})
				if err == nil && (updated.NextRunAt == nil || !updated.NextRunAt.Equal(fireTime)) {
					t.Fatalf("测试要求修改表达式后仍保留同一触发时刻：%v", updated.NextRunAt)
				}
			case "delete":
				err = service.DeleteSchedule(ctx, definition.ID, schedule.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-finished:
				if result.err != nil || len(result.runs) != 1 || result.runs[0].DefinitionID != otherDefinition.ID {
					t.Fatalf("旧计划必须跳过且不能阻断其他计划：runs=%+v err=%v", result.runs, result.err)
				}
			case <-ctx.Done():
				t.Fatal("扫描计划未结束：", ctx.Err())
			}
			var count int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM task_runs WHERE task_definition_id=$1`, definition.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("已返回成功的计划变更后不得创建旧实例：count=%d err=%v", count, err)
			}
		})
	}
}

func TestScheduleDueAdvancesOverlappingPlansAfterDeduplication(t *testing.T) {
	db := taskDatabase(t)
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	definition, err := service.Create(ctx, validTaskInput(scriptID, userID, "重叠计划"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{"0 2 * * *", "0 2 * * 5"} {
		if _, err := service.CreateSchedule(ctx, task.ScheduleInput{DefinitionID: definition.ID, CronExpression: expression, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	fireTime := mustTaskTime(t, "2026-08-28T02:00:00+08:00")
	runs, err := service.ScheduleDue(ctx, fireTime)
	if err != nil || len(runs) != 1 {
		t.Fatalf("同一任务同一火点只能创建一个实例：runs=%v err=%v", runs, err)
	}
	schedules, err := service.ListSchedules(ctx, definition.ID)
	if err != nil || len(schedules) != 2 {
		t.Fatalf("读取重叠计划：%v %v", schedules, err)
	}
	for _, schedule := range schedules {
		if schedule.NextRunAt == nil || !schedule.NextRunAt.After(fireTime) {
			t.Fatalf("去重的计划也必须推进：%+v", schedule)
		}
	}
}

func waitForScheduleDefinitionLock(t *testing.T, ctx context.Context, db *pgxpool.Pool, blockerPID uint32) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE $1::int=ANY(pg_blocking_pids(pid)) AND query LIKE '%FROM task_definitions%'
			)
		`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("调度未到达预期任务锁：", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestScheduleDueContinuesAfterInvalidParameters(t *testing.T) {
	db := taskDatabase(t)
	ctx := context.Background()
	userID := insertTaskUser(t, db)
	scriptID := insertTaskScript(t, db, userID)
	_ = insertTaskVersion(t, db, scriptID, userID, 1)
	service := task.NewService(db, taskClock)
	invalidDefinition, err := service.Create(ctx, validTaskInput(scriptID, userID, "旧任务缺少新版本必填参数"))
	if err != nil {
		t.Fatal(err)
	}
	validInput := validTaskInput(scriptID, userID, "正常的后续任务")
	validInput.Parameters = map[string]any{"customer": "A"}
	validDefinition, err := service.Create(ctx, validInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO script_versions (script_id,version,artifact_uri,artifact_sha256,entrypoint,manifest,release_notes,created_by)
		VALUES ($1,2,'scripts/required-v2.tar.gz',repeat('b',64),'main.sh',
		'{"runtime":"bash","parameterDefinitions":[{"name":"customer","type":"string","required":true}]}','新增必填参数',$2)
	`, scriptID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSchedule(ctx, task.ScheduleInput{DefinitionID: invalidDefinition.ID, CronExpression: "0 2 * * *", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSchedule(ctx, task.ScheduleInput{DefinitionID: validDefinition.ID, CronExpression: "1 2 * * *", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	runs, err := service.ScheduleDue(ctx, mustTaskTime(t, "2026-08-28T02:01:00+08:00"))
	if !errors.Is(err, task.ErrInvalidParameters) || len(runs) != 1 || runs[0].DefinitionID != validDefinition.ID {
		t.Fatalf("参数无效应报告错误并继续创建正常计划实例：runs=%+v err=%v", runs, err)
	}
	var invalidRuns int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM task_runs WHERE task_definition_id=$1`, invalidDefinition.ID).Scan(&invalidRuns); err != nil || invalidRuns != 0 {
		t.Fatalf("无效参数不得生成实例：count=%d err=%v", invalidRuns, err)
	}
}
