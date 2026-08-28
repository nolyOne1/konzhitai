package task

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"yunling.local/platform/internal/auth"
)

type Manager interface {
	Create(context.Context, CreateInput) (Definition, error)
	Update(context.Context, string, CreateInput) (Definition, error)
	Get(context.Context, string) (Definition, error)
	List(context.Context) ([]Definition, error)
	Delete(context.Context, string) error
	SetEnabled(context.Context, string, bool, bool) error
	Trigger(context.Context, string, Trigger) (Run, error)
	CreateSchedule(context.Context, ScheduleInput) (Schedule, error)
	ListSchedules(context.Context, string) ([]Schedule, error)
	DeleteSchedule(context.Context, string, string) error
}

func Handler(manager Manager) http.Handler {
	router := http.NewServeMux()
	router.Handle("GET /api/tasks", auth.Require(auth.PermissionRead)(listTasksHandler(manager)))
	router.Handle("POST /api/tasks", auth.Require(auth.PermissionExecute)(createTaskHandler(manager)))
	router.Handle("POST /api/tasks/cron/validate", auth.Require(auth.PermissionExecute)(validateCronHandler()))
	router.Handle("GET /api/tasks/{id}", auth.Require(auth.PermissionRead)(getTaskHandler(manager)))
	router.Handle("PUT /api/tasks/{id}", auth.Require(auth.PermissionExecute)(updateTaskHandler(manager)))
	router.Handle("DELETE /api/tasks/{id}", auth.Require(auth.PermissionExecute)(deleteTaskHandler(manager)))
	router.Handle("PATCH /api/tasks/{id}/enabled", auth.Require(auth.PermissionExecute)(setTaskEnabledHandler(manager)))
	router.Handle("POST /api/tasks/{id}/run", auth.Require(auth.PermissionExecute)(runTaskHandler(manager)))
	router.Handle("GET /api/tasks/{id}/schedules", auth.Require(auth.PermissionRead)(listSchedulesHandler(manager)))
	router.Handle("POST /api/tasks/{id}/schedules", auth.Require(auth.PermissionExecute)(createScheduleHandler(manager)))
	router.Handle("DELETE /api/tasks/{id}/schedules/{scheduleID}", auth.Require(auth.PermissionExecute)(deleteScheduleHandler(manager)))
	return router
}

func listTasksHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		definitions, err := manager.List(r.Context())
		if err != nil {
			writeTaskError(w, http.StatusInternalServerError, "读取任务列表失败")
			return
		}
		if definitions == nil {
			definitions = []Definition{}
		}
		writeTaskJSON(w, http.StatusOK, map[string]any{"tasks": definitions})
	})
}

func createTaskHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CreateInput
		if !decodeTaskJSON(w, r, &input) {
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		input.CreatedBy = principal.UserID
		definition, err := manager.Create(r.Context(), input)
		if writeTaskServiceError(w, err, "创建任务失败") {
			return
		}
		writeTaskJSON(w, http.StatusCreated, definition)
	})
}

func getTaskHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		definition, err := manager.Get(r.Context(), r.PathValue("id"))
		if writeTaskServiceError(w, err, "读取任务失败") {
			return
		}
		writeTaskJSON(w, http.StatusOK, definition)
	})
}

func updateTaskHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CreateInput
		if !decodeTaskJSON(w, r, &input) {
			return
		}
		definition, err := manager.Update(r.Context(), r.PathValue("id"), input)
		if writeTaskServiceError(w, err, "更新任务失败") {
			return
		}
		writeTaskJSON(w, http.StatusOK, definition)
	})
}

func deleteTaskHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeTaskServiceError(w, manager.Delete(r.Context(), r.PathValue("id")), "删除任务失败") {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func setTaskEnabledHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled      bool `json:"enabled"`
			CancelQueued bool `json:"cancelQueued"`
		}
		if !decodeTaskJSON(w, r, &input) {
			return
		}
		if writeTaskServiceError(w, manager.SetEnabled(r.Context(), r.PathValue("id"), input.Enabled, input.CancelQueued), "停启任务失败") {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func runTaskHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Parameters map[string]any `json:"parameters"`
		}
		if !decodeTaskJSON(w, r, &input) {
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		run, err := manager.Trigger(r.Context(), r.PathValue("id"), Trigger{
			Type: TriggerManual, RequestedBy: principal.UserID, Parameters: input.Parameters,
		})
		if writeTaskServiceError(w, err, "创建运行实例失败") {
			return
		}
		writeTaskJSON(w, http.StatusCreated, run)
	})
}

func listSchedulesHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		schedules, err := manager.ListSchedules(r.Context(), r.PathValue("id"))
		if err != nil {
			writeTaskError(w, http.StatusInternalServerError, "读取定时计划失败")
			return
		}
		if schedules == nil {
			schedules = []Schedule{}
		}
		writeTaskJSON(w, http.StatusOK, map[string]any{"schedules": schedules})
	})
}

func createScheduleHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input ScheduleInput
		if !decodeTaskJSON(w, r, &input) {
			return
		}
		input.DefinitionID = r.PathValue("id")
		schedule, err := manager.CreateSchedule(r.Context(), input)
		if writeTaskServiceError(w, err, "创建定时计划失败") {
			return
		}
		writeTaskJSON(w, http.StatusCreated, schedule)
	})
}

func deleteScheduleHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := manager.DeleteSchedule(r.Context(), r.PathValue("id"), r.PathValue("scheduleID"))
		if writeTaskServiceError(w, err, "删除定时计划失败") {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func validateCronHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			CronExpression string `json:"cronExpression"`
			Timezone       string `json:"timezone"`
		}
		if !decodeTaskJSON(w, r, &input) {
			return
		}
		if err := ValidateCron(input.CronExpression, input.Timezone); err != nil {
			writeTaskError(w, http.StatusBadRequest, ErrInvalidCron.Error())
			return
		}
		writeTaskJSON(w, http.StatusOK, map[string]bool{"valid": true})
	})
}

func decodeTaskJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeTaskError(w, http.StatusBadRequest, "请求内容格式不正确")
		return false
	}
	return true
}

func writeTaskServiceError(w http.ResponseWriter, err error, fallback string) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrDefinitionNotFound):
		writeTaskError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrDefinitionDisabled), errors.Is(err, ErrVersionUnavailable), errors.Is(err, ErrDuplicateRun):
		writeTaskError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidDefinition), errors.Is(err, ErrInvalidCron):
		writeTaskError(w, http.StatusBadRequest, err.Error())
	default:
		writeTaskError(w, http.StatusInternalServerError, fallback)
	}
	return true
}

func writeTaskError(w http.ResponseWriter, status int, message string) {
	writeTaskJSON(w, status, map[string]string{"message": message})
}

func writeTaskJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
