package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"yunling.local/platform/internal/auth"
)

func RunHandler(manager RunManager) http.Handler {
	router := http.NewServeMux()
	router.Handle("GET /api/runs", auth.Require(auth.PermissionRead)(listRunsHandler(manager)))
	router.Handle("GET /api/runs/{id}", auth.Require(auth.PermissionRead)(getRunHandler(manager)))
	router.Handle("GET /api/runs/{id}/events", auth.Require(auth.PermissionRead)(runEventsHandler(manager)))
	router.Handle("GET /api/runs/{id}/logs", auth.Require(auth.PermissionRead)(downloadRunLogsHandler(manager)))
	router.Handle("POST /api/runs/{id}/cancel", auth.Require(auth.PermissionExecute)(cancelRunHandler(manager)))
	router.Handle("POST /api/runs/{id}/retry", auth.Require(auth.PermissionExecute)(retryRunHandler(manager)))
	return router
}

func listRunsHandler(manager RunManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := parseRunFilter(r.URL.Query())
		if err != nil {
			writeTaskError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, err := manager.QueryRuns(r.Context(), filter)
		if err != nil {
			writeTaskError(w, http.StatusInternalServerError, "读取执行记录失败")
			return
		}
		if page.Runs == nil {
			page.Runs = []RunView{}
		}
		writeTaskJSON(w, http.StatusOK, page)
	}
}

var runFilterUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func parseRunFilter(values url.Values) (RunFilter, error) {
	filter := RunFilter{Query: strings.TrimSpace(values.Get("query")), State: RunState(values.Get("state")),
		DefinitionID: values.Get("taskId"), ScriptID: values.Get("scriptId"), ServerID: values.Get("serverId"), Limit: 50}
	invalid := errors.New("执行记录筛选条件无效")
	if len([]rune(filter.Query)) > 200 {
		return filter, invalid
	}
	if filter.State != "" {
		switch filter.State {
		case Queued, Scheduling, Assigned, Syncing, Running, Succeeded, Failed, TimedOut, Cancelled, Expired, Unknown:
		default:
			return filter, invalid
		}
	}
	for _, id := range []string{filter.DefinitionID, filter.ScriptID, filter.ServerID} {
		if id != "" && !runFilterUUID.MatchString(id) {
			return filter, invalid
		}
	}
	for key, target := range map[string]**time.Time{"from": &filter.From, "until": &filter.Until} {
		if value := values.Get(key); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return filter, invalid
			}
			*target = &parsed
		}
	}
	if filter.From != nil && filter.Until != nil && !filter.From.Before(*filter.Until) {
		return filter, invalid
	}
	for key, target := range map[string]*int{"limit": &filter.Limit, "offset": &filter.Offset} {
		if value := values.Get(key); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 32)
			if err != nil {
				return filter, invalid
			}
			*target = int(parsed)
		}
	}
	if filter.Limit < 1 || filter.Limit > 200 || filter.Offset < 0 {
		return filter, invalid
	}
	return filter, nil
}

func downloadRunLogsHandler(manager RunManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		events, err := manager.ListRunEvents(r.Context(), r.PathValue("id"))
		if writeRunError(w, err, "读取完整日志失败") {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="run.log"`)
		w.Header().Set("Cache-Control", "no-store")
		for _, event := range events {
			if event.Kind != "log" {
				continue
			}
			if _, err := fmt.Fprintf(w, "[%s] [%s] %s", event.OccurredAt.Format(time.RFC3339Nano), event.Stream, event.Content); err != nil {
				return
			}
			if !strings.HasSuffix(event.Content, "\n") {
				if _, err := fmt.Fprintln(w); err != nil {
					return
				}
			}
		}
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
