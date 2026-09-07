package agentupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"yunling.local/platform/internal/auth"
)

type Management interface {
	CreatePlan(context.Context, CreatePlanInput) (Plan, error)
	ListPlans(context.Context) ([]Plan, error)
	Plan(context.Context, string) (Plan, error)
	Pause(context.Context, string, string) (Plan, error)
	Resume(context.Context, string) (Plan, error)
	Cancel(context.Context, string) (Plan, error)
	RetryTarget(context.Context, string, string) (Plan, error)
	CreateRollbackPlan(context.Context, string, string, string) (Plan, error)
}

func ManagementHandler(service Management) http.Handler {
	router := http.NewServeMux()
	router.Handle("GET /api/agent-upgrades", auth.Require(auth.PermissionRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plans, err := service.ListPlans(r.Context())
		if err != nil {
			writeUpgradeError(w, http.StatusInternalServerError, "读取代理升级计划失败")
			return
		}
		if plans == nil {
			plans = []Plan{}
		}
		writeUpgradeJSON(w, http.StatusOK, map[string]any{"plans": plans})
	})))
	router.Handle("POST /api/agent-upgrades", auth.Require(auth.PermissionAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CreatePlanInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeUpgradeError(w, http.StatusBadRequest, "升级计划内容格式无效")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		input.CreatedBy = principal.UserID
		plan, err := service.CreatePlan(r.Context(), input)
		writeUpgradeResult(w, plan, err, http.StatusCreated)
	})))
	router.Handle("GET /api/agent-upgrades/{id}", auth.Require(auth.PermissionRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plan, err := service.Plan(r.Context(), r.PathValue("id"))
		writeUpgradeResult(w, plan, err, http.StatusOK)
	})))
	for action, mutate := range map[string]func(context.Context, string) (Plan, error){
		"resume": service.Resume, "cancel": service.Cancel,
	} {
		action, mutate := action, mutate
		router.Handle("POST /api/agent-upgrades/{id}/"+action, auth.Require(auth.PermissionAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plan, err := mutate(r.Context(), r.PathValue("id"))
			writeUpgradeResult(w, plan, err, http.StatusOK)
		})))
	}
	router.Handle("POST /api/agent-upgrades/{id}/pause", auth.Require(auth.PermissionAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Reason string `json:"reason"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&input)
		}
		plan, err := service.Pause(r.Context(), r.PathValue("id"), input.Reason)
		writeUpgradeResult(w, plan, err, http.StatusOK)
	})))
	router.Handle("POST /api/agent-upgrades/{id}/targets/{target}/retry", auth.Require(auth.PermissionAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plan, err := service.RetryTarget(r.Context(), r.PathValue("id"), r.PathValue("target"))
		writeUpgradeResult(w, plan, err, http.StatusOK)
	})))
	router.Handle("POST /api/agent-upgrades/{id}/targets/{target}/rollback", auth.Require(auth.PermissionAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ := auth.PrincipalFromContext(r.Context())
		plan, err := service.CreateRollbackPlan(r.Context(), r.PathValue("id"), r.PathValue("target"), principal.UserID)
		writeUpgradeResult(w, plan, err, http.StatusCreated)
	})))
	return router
}

func writeUpgradeResult(w http.ResponseWriter, plan Plan, err error, success int) {
	switch {
	case errors.Is(err, ErrPlanNotFound), errors.Is(err, ErrTargetNotFound), errors.Is(err, ErrReleaseNotFound):
		writeUpgradeError(w, http.StatusNotFound, "代理升级计划或目标不存在")
	case errors.Is(err, ErrInvalidPlan), errors.Is(err, ErrServerIneligible), errors.Is(err, ErrUpgradeUnsupported), errors.Is(err, ErrArtifactUnavailable), errors.Is(err, ErrNoUpgradeNeeded):
		writeUpgradeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrActivePlanExists), errors.Is(err, ErrInvalidTransition):
		writeUpgradeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeUpgradeError(w, http.StatusInternalServerError, "处理代理升级计划失败")
	default:
		writeUpgradeJSON(w, success, plan)
	}
}
func writeUpgradeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeUpgradeError(w http.ResponseWriter, status int, message string) {
	writeUpgradeJSON(w, status, map[string]string{"message": message})
}
