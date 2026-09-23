package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"yunling.local/platform/internal/auth"
)

func GroupsHandler(manager GroupManager) http.Handler {
	router := http.NewServeMux()
	router.Handle("GET /api/server-groups", auth.Require(auth.PermissionRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		groups, err := manager.ListGroups(r.Context())
		if err != nil {
			writeServerError(w, http.StatusInternalServerError, "读取服务器组失败")
			return
		}
		if groups == nil {
			groups = []Group{}
		}
		writeServerJSON(w, http.StatusOK, map[string]any{"groups": groups})
	})))
	mutate := func(create bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				Name string `json:"name"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeServerError(w, http.StatusBadRequest, "服务器组内容格式无效")
				return
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				writeServerError(w, http.StatusBadRequest, "服务器组内容格式无效")
				return
			}
			name, err := normalizedGroupName(input.Name)
			if err != nil {
				writeServerError(w, http.StatusBadRequest, err.Error())
				return
			}
			var group Group
			status := http.StatusOK
			if create {
				group, err = manager.CreateGroup(r.Context(), name)
				status = http.StatusCreated
			} else {
				group, err = manager.RenameGroup(r.Context(), r.PathValue("id"), name)
			}
			switch {
			case errors.Is(err, ErrInvalidGroup):
				writeServerError(w, http.StatusBadRequest, err.Error())
			case errors.Is(err, ErrGroupNotFound):
				writeServerError(w, http.StatusNotFound, err.Error())
			case errors.Is(err, ErrGroupNameExists):
				writeServerError(w, http.StatusConflict, err.Error())
			case err != nil:
				writeServerError(w, http.StatusInternalServerError, "保存服务器组失败")
			default:
				writeServerJSON(w, status, group)
			}
		}
	}
	router.Handle("POST /api/server-groups", auth.Require(auth.PermissionExecute)(mutate(true)))
	router.Handle("PATCH /api/server-groups/{id}", auth.Require(auth.PermissionExecute)(mutate(false)))
	return router
}
