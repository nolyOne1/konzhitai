package audit

import (
	"net"
	"net/http"
	"strings"

	"yunling.local/platform/internal/auth"
)

func Middleware(service *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			action, targetType, targetID, auditable := classifyRequest(r.Method, r.URL.Path)
			if !auditable || service == nil {
				next.ServeHTTP(w, r)
				return
			}
			writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(writer, r)
			if writer.status < http.StatusOK || writer.status >= http.StatusBadRequest {
				return
			}
			principal, _ := auth.PrincipalFromContext(r.Context())
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			_ = service.Record(r.Context(), Event{
				ActorID: principal.UserID, Action: action, TargetType: targetType,
				TargetID: targetID, IPAddress: ip,
			})
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	return w.ResponseWriter.Write(body)
}

func classifyRequest(method, path string) (string, string, string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if method == http.MethodPost && path == "/api/auth/login" {
		return "auth.login", "session", "login", true
	}
	if len(parts) >= 4 && parts[0] == "api" {
		switch parts[1] {
		case "scripts":
			if method == http.MethodPost && len(parts) == 6 && parts[3] == "syncs" && parts[5] == "retry" {
				return "script.sync.retry", "script_sync", parts[4], true
			}
		case "tasks":
			if method == http.MethodPost && len(parts) == 4 && parts[3] == "run" {
				return "task.run", "task", parts[2], true
			}
			if method == http.MethodPatch && len(parts) == 4 && parts[3] == "enabled" {
				return "task.enabled.update", "task", parts[2], true
			}
		case "runs":
			if method == http.MethodPost && len(parts) == 4 && (parts[3] == "cancel" || parts[3] == "retry") {
				return "run." + parts[3], "run", parts[2], true
			}
		}
	}
	if len(parts) == 2 && parts[0] == "api" && parts[1] == "tasks" && method == http.MethodPost {
		return "task.create", "task", "new", true
	}
	return "", "", "", false
}
