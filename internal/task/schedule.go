package task

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrInvalidCron = errors.New("Cron 规则或时区无效")

var errScheduleChanged = errors.New("定时计划已变更")

type dueSchedule struct {
	id, definitionID, expression, timezone string
	fireTime, updatedAt                    time.Time
}

const defaultTimezone = "Asia/Shanghai"

func ValidateCron(expression, timezone string) error {
	if strings.TrimSpace(timezone) == "" {
		timezone = defaultTimezone
	}
	// "Local" depends on the API host and is not an explicit IANA timezone.
	if timezone == "Local" {
		return ErrInvalidCron
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return ErrInvalidCron
	}
	_, err := parseCron(expression)
	return err
}

func (s *Service) CreateSchedule(ctx context.Context, input ScheduleInput) (Schedule, error) {
	input.DefinitionID = strings.TrimSpace(input.DefinitionID)
	input.CronExpression = strings.TrimSpace(input.CronExpression)
	input.Timezone = strings.TrimSpace(input.Timezone)
	if input.Timezone == "" {
		input.Timezone = defaultTimezone
	}
	if input.DefinitionID == "" || ValidateCron(input.CronExpression, input.Timezone) != nil {
		return Schedule{}, ErrInvalidCron
	}
	next, err := nextCronTime(input.CronExpression, input.Timezone, s.now())
	if err != nil {
		return Schedule{}, err
	}
	var schedule Schedule
	err = s.db.QueryRow(ctx, `
		INSERT INTO task_schedules (
			task_definition_id, cron_expression, timezone, enabled, next_run_at
		)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, task_definition_id, cron_expression, timezone, enabled,
		          next_run_at, created_at, updated_at
	`, input.DefinitionID, input.CronExpression, input.Timezone, input.Enabled, next).Scan(
		&schedule.ID, &schedule.DefinitionID, &schedule.CronExpression,
		&schedule.Timezone, &schedule.Enabled, &schedule.NextRunAt,
		&schedule.CreatedAt, &schedule.UpdatedAt,
	)
	if err != nil {
		return Schedule{}, fmt.Errorf("创建定时计划：%w", err)
	}
	return schedule, nil
}

func (s *Service) ListSchedules(ctx context.Context, definitionID string) ([]Schedule, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, task_definition_id, cron_expression, timezone, enabled,
		       next_run_at, created_at, updated_at
		FROM task_schedules
		WHERE task_definition_id=$1
		ORDER BY created_at
	`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("读取定时计划：%w", err)
	}
	defer rows.Close()
	schedules := []Schedule{}
	for rows.Next() {
		var schedule Schedule
		if err := rows.Scan(&schedule.ID, &schedule.DefinitionID, &schedule.CronExpression,
			&schedule.Timezone, &schedule.Enabled, &schedule.NextRunAt,
			&schedule.CreatedAt, &schedule.UpdatedAt); err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (s *Service) UpdateSchedule(ctx context.Context, scheduleID string, input ScheduleInput) (Schedule, error) {
	input.CronExpression = strings.TrimSpace(input.CronExpression)
	input.Timezone = strings.TrimSpace(input.Timezone)
	if input.Timezone == "" {
		input.Timezone = defaultTimezone
	}
	if input.DefinitionID == "" || ValidateCron(input.CronExpression, input.Timezone) != nil {
		return Schedule{}, ErrInvalidCron
	}
	next, err := nextCronTime(input.CronExpression, input.Timezone, s.now())
	if err != nil {
		return Schedule{}, err
	}
	var schedule Schedule
	err = s.db.QueryRow(ctx, `
		UPDATE task_schedules SET cron_expression=$3, timezone=$4, enabled=$5,
		       next_run_at=$6, updated_at=$7
		WHERE id=$1 AND task_definition_id=$2
		RETURNING id, task_definition_id, cron_expression, timezone, enabled,
		          next_run_at, created_at, updated_at
	`, scheduleID, input.DefinitionID, input.CronExpression, input.Timezone, input.Enabled, next, s.now()).Scan(
		&schedule.ID, &schedule.DefinitionID, &schedule.CronExpression,
		&schedule.Timezone, &schedule.Enabled, &schedule.NextRunAt, &schedule.CreatedAt, &schedule.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrDefinitionNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("更新定时计划：%w", err)
	}
	return schedule, nil
}

func (s *Service) DeleteSchedule(ctx context.Context, definitionID, scheduleID string) error {
	command, err := s.db.Exec(ctx, `DELETE FROM task_schedules WHERE id=$1 AND task_definition_id=$2`, scheduleID, definitionID)
	if err != nil {
		return fmt.Errorf("删除定时计划：%w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrDefinitionNotFound
	}
	return nil
}

func (s *Service) ScheduleDue(ctx context.Context, now time.Time) ([]Run, error) {
	rows, err := s.db.Query(ctx, `
		SELECT schedule.id, schedule.task_definition_id, schedule.cron_expression,
		       schedule.timezone, schedule.next_run_at, schedule.updated_at
		FROM task_schedules AS schedule
		JOIN task_definitions AS definition ON definition.id=schedule.task_definition_id
		WHERE schedule.enabled=true AND definition.enabled=true
		  AND schedule.next_run_at IS NOT NULL AND schedule.next_run_at <= $1
		ORDER BY schedule.next_run_at, schedule.id
	`, now)
	if err != nil {
		return nil, fmt.Errorf("读取到期计划：%w", err)
	}
	due := []dueSchedule{}
	for rows.Next() {
		var item dueSchedule
		if err := rows.Scan(&item.id, &item.definitionID, &item.expression, &item.timezone, &item.fireTime, &item.updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	runs := []Run{}
	var failures []error
	for _, item := range due {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		fireTime := item.fireTime
		run, triggerErr := s.trigger(ctx, item.definitionID, Trigger{Type: TriggerSchedule, ScheduledFor: &fireTime}, &item)
		if triggerErr == nil {
			runs = append(runs, run)
		} else if !errors.Is(triggerErr, ErrDuplicateRun) && !errors.Is(triggerErr, errScheduleChanged) &&
			!errors.Is(triggerErr, ErrDefinitionDisabled) && !errors.Is(triggerErr, ErrDefinitionNotFound) {
			failures = append(failures, fmt.Errorf("定时计划 %s：%w", item.id, triggerErr))
		}
	}
	return runs, errors.Join(failures...)
}

// Lock after the definition so disabling/deleting a definition and triggering a
// schedule always acquire locks in the same order. The scanned schedule may have
// changed while this transaction was waiting for the definition lock.
func lockDueSchedule(ctx context.Context, tx pgx.Tx, expected *dueSchedule) (*time.Time, error) {
	if expected == nil {
		return nil, nil
	}
	var expression, timezone string
	var enabled bool
	var fireTime *time.Time
	var updatedAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT cron_expression, timezone, enabled, next_run_at, updated_at
		FROM task_schedules WHERE id=$1 AND task_definition_id=$2 FOR UPDATE
	`, expected.id, expected.definitionID).Scan(&expression, &timezone, &enabled, &fireTime, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errScheduleChanged
	}
	if err != nil {
		return nil, fmt.Errorf("锁定定时计划：%w", err)
	}
	if !enabled || fireTime == nil || !fireTime.Equal(expected.fireTime) ||
		expression != expected.expression || timezone != expected.timezone || !updatedAt.Equal(expected.updatedAt) {
		return nil, errScheduleChanged
	}
	next, err := nextCronTime(expression, timezone, *fireTime)
	return &next, err
}

func advanceDueSchedule(ctx context.Context, tx pgx.Tx, schedule *dueSchedule, next *time.Time, now time.Time) error {
	if schedule == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE task_schedules SET next_run_at=$2, updated_at=$3 WHERE id=$1`, schedule.id, next, now); err != nil {
		return fmt.Errorf("推进定时计划：%w", err)
	}
	return nil
}

type cronSpec struct {
	minute, hour, day, month, weekday map[int]bool
	dayWildcard, weekdayWildcard      bool
}

func parseCron(expression string) (cronSpec, error) {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return cronSpec{}, ErrInvalidCron
	}
	minute, minuteWildcard, err := parseCronField(fields[0], 0, 59, false)
	if err != nil {
		return cronSpec{}, ErrInvalidCron
	}
	hour, hourWildcard, err := parseCronField(fields[1], 0, 23, false)
	if err != nil {
		return cronSpec{}, ErrInvalidCron
	}
	_ = hourWildcard
	day, dayWildcard, err := parseCronField(fields[2], 1, 31, false)
	if err != nil {
		return cronSpec{}, ErrInvalidCron
	}
	month, monthWildcard, err := parseCronField(fields[3], 1, 12, false)
	if err != nil {
		return cronSpec{}, ErrInvalidCron
	}
	_ = monthWildcard
	weekday, weekdayWildcard, err := parseCronField(fields[4], 0, 7, true)
	if err != nil {
		return cronSpec{}, ErrInvalidCron
	}
	_ = minuteWildcard
	return cronSpec{minute: minute, hour: hour, day: day, month: month, weekday: weekday, dayWildcard: dayWildcard, weekdayWildcard: weekdayWildcard}, nil
}

func parseCronField(value string, minimum, maximum int, normalizeSunday bool) (map[int]bool, bool, error) {
	selected := map[int]bool{}
	wildcard := value == "*"
	for _, part := range strings.Split(value, ",") {
		base := part
		step := 1
		if strings.Contains(part, "/") {
			pieces := strings.Split(part, "/")
			if len(pieces) != 2 {
				return nil, false, ErrInvalidCron
			}
			base = pieces[0]
			parsedStep, err := strconv.Atoi(pieces[1])
			if err != nil || parsedStep <= 0 {
				return nil, false, ErrInvalidCron
			}
			step = parsedStep
		}
		start, end := minimum, maximum
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				return nil, false, ErrInvalidCron
			}
			var err error
			start, err = strconv.Atoi(bounds[0])
			if err != nil {
				return nil, false, ErrInvalidCron
			}
			end, err = strconv.Atoi(bounds[1])
			if err != nil {
				return nil, false, ErrInvalidCron
			}
		default:
			parsed, err := strconv.Atoi(base)
			if err != nil || step != 1 {
				return nil, false, ErrInvalidCron
			}
			start, end = parsed, parsed
		}
		if start < minimum || end > maximum || start > end {
			return nil, false, ErrInvalidCron
		}
		for item := start; item <= end; item += step {
			selectedItem := item
			if normalizeSunday && selectedItem == 7 {
				selectedItem = 0
			}
			selected[selectedItem] = true
		}
	}
	if len(selected) == 0 {
		return nil, false, ErrInvalidCron
	}
	return selected, wildcard, nil
}

func nextCronTime(expression, timezone string, after time.Time) (time.Time, error) {
	spec, err := parseCron(expression)
	if err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, ErrInvalidCron
	}
	candidate := after.In(location).Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(5, 0, 0)
	for candidate.Before(limit) {
		dayMatches := spec.day[candidate.Day()]
		weekdayMatches := spec.weekday[int(candidate.Weekday())]
		calendarMatches := false
		switch {
		case spec.dayWildcard && spec.weekdayWildcard:
			calendarMatches = true
		case spec.dayWildcard:
			calendarMatches = weekdayMatches
		case spec.weekdayWildcard:
			calendarMatches = dayMatches
		default:
			calendarMatches = dayMatches || weekdayMatches
		}
		if spec.minute[candidate.Minute()] && spec.hour[candidate.Hour()] &&
			spec.month[int(candidate.Month())] && calendarMatches {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, ErrInvalidCron
}

var _ scanner = pgx.Row(nil)
