package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"yunling.local/platform/internal/auth"
)

func RunHandler(manager RunManager) http.Handler {
	router := http.NewServeMux()
	router.Handle("GET /api/runs", auth.Require(auth.PermissionRead)(listRunsHandler(manager)))
	router.Handle("GET /api/runs/{id}", auth.Require(auth.PermissionRead)(getRunHandler(manager)))
	router.Handle("GET /api/runs/{id}/events", auth.Require(auth.PermissionRead)(runEventsHandler(manager)))
	router.Handle("POST /api/runs/{id}/cancel", auth.Require(auth.PermissionExecute)(cancelRunHandler(manager)))
	router.Handle("POST /api/runs/{id}/retry", auth.Require(auth.PermissionExecute)(retryRunHandler(manager)))
	return router
}

func listRunsHandler(manager RunManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runs, err := manager.ListRuns(r.Context())
		if err != nil {
			writeTaskError(w, http.StatusInternalServerError, "读取执行记录失败")
			return
		}
		if runs == nil {
			runs = []RunView{}
		}
		writeTaskJSON(w, http.StatusOK, map[string]any{"runs": runs})
	}
}

func getRunHandler(manager RunManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		run, err := manager.GetRun(r.Context(), r.PathValue("id"))
		if writeRunError(w, err, "读取执行详情失败") {
			return
		}
		writeTaskJSON(w, http.StatusOK, run)
	}
}

func cancelRunHandler(manager RunManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if writeRunError(w, manager.CancelRun(r.Context(), r.PathValue("id")), "取消任务失败") {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func retryRunHandler(manager RunManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, err := manager.RetryRun(r.Context(), r.PathValue("id"))
		if writeRunError(w, err, "重试任务失败") {
			return
		}
		writeTaskJSON(w, http.StatusCreated, map[string]any{"id": runID})
	}
}

func runEventsHandler(manager RunManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		initial, err := manager.ListRunEvents(r.Context(), r.PathValue("id"))
		if writeRunError(w, err, "读取实时事件失败") {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeTaskError(w, http.StatusInternalServerError, "当前连接不支持实时事件")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
		seen := map[string]bool{}
		sendEvents := func(events []RunStreamEvent) error {
			for _, event := range events {
				if seen[event.ID] {
					continue
				}
				body, _ := json.Marshal(event)
				if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Kind, body); err != nil {
					return err
				}
				seen[event.ID] = true
			}
			flusher.Flush()
			return nil
		}
		if err := sendEvents(initial); err != nil {
			return
		}
		if r.URL.Query().Get("follow") == "false" {
			return
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				events, err := manager.ListRunEvents(r.Context(), r.PathValue("id"))
				if err != nil || sendEvents(events) != nil {
					return
				}
			}
		}
	}
}

func writeRunError(w http.ResponseWriter, err error, fallback string) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrRunNotFound):
		writeTaskError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrRunNotRetryable), errors.Is(err, ErrRunNotCancellable), errors.Is(err, ErrRunCommandUnavailable):
		writeTaskError(w, http.StatusConflict, err.Error())
	default:
		writeTaskError(w, http.StatusInternalServerError, fallback)
	}
	return true
}
