package agentrelease

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"yunling.local/platform/internal/auth"
)

type Management interface {
	List(context.Context) ([]Release, error)
	SetRecommended(context.Context, string) (Release, error)
	Withdraw(context.Context, string) (Release, error)
}

func ManagementHandler(service Management) http.Handler {
	router := http.NewServeMux()
	router.Handle("GET /api/agent-releases", auth.Require(auth.PermissionRead)(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		releases, err := service.List(request.Context())
		if err != nil {
			writeManagementError(response, http.StatusInternalServerError, "读取代理版本失败")
			return
		}
		writeManagementJSON(response, http.StatusOK, map[string]any{"releases": releases})
	})))
	router.Handle("POST /api/agent-releases/{id}/recommend", auth.Require(auth.PermissionAdmin)(releaseMutationHandler(service.SetRecommended)))
	router.Handle("POST /api/agent-releases/{id}/withdraw", auth.Require(auth.PermissionAdmin)(releaseMutationHandler(service.Withdraw)))
	return router
}

func releaseMutationHandler(mutate func(context.Context, string) (Release, error)) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		release, err := mutate(request.Context(), request.PathValue("id"))
		switch {
		case errors.Is(err, ErrReleaseNotFound):
			writeManagementError(response, http.StatusNotFound, "代理版本不存在")
		case errors.Is(err, ErrReleaseWithdrawn):
			writeManagementError(response, http.StatusConflict, "已撤回的代理版本不能设为推荐版本")
		case errors.Is(err, ErrRecommendedRelease):
			writeManagementError(response, http.StatusConflict, "当前推荐版本不能撤回")
		case err != nil:
			writeManagementError(response, http.StatusInternalServerError, "更新代理版本失败")
		default:
			writeManagementJSON(response, http.StatusOK, release)
		}
	})
}

func writeManagementJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeManagementError(response http.ResponseWriter, status int, message string) {
	writeManagementJSON(response, status, map[string]string{"message": message})
}
