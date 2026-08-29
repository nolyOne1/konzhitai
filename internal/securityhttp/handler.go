package securityhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"yunling.local/platform/internal/alert"
	"yunling.local/platform/internal/audit"
	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/secret"
	"yunling.local/platform/internal/server"
)

type SecretManager interface {
	Create(context.Context, string, []byte) (secret.Metadata, error)
	List(context.Context) ([]secret.Metadata, error)
}

type AuditManager interface {
	Record(context.Context, audit.Event) error
	List(context.Context, audit.Filter) ([]audit.Event, error)
}

type AlertManager interface {
	List(context.Context) ([]alert.Alert, error)
	Acknowledge(context.Context, string, string) error
}

type TeamManager interface {
	List(context.Context) ([]auth.Member, error)
	UpdateRoles(context.Context, string, []auth.RoleName) (auth.Member, error)
}

type CredentialManager interface {
	Rotate(context.Context, string) (server.AgentCredentials, error)
	Revoke(context.Context, string) error
}

type Services struct {
	Secrets     SecretManager
	Audits      AuditManager
	Alerts      AlertManager
	Team        TeamManager
	Credentials CredentialManager
}

func NewHandler(services Services) http.Handler {
	router := http.NewServeMux()
	router.Handle("GET /api/secrets", auth.Require(auth.PermissionRead)(listSecrets(services.Secrets)))
	router.Handle("POST /api/secrets", auth.Require(auth.PermissionAdmin)(createSecret(services.Secrets, services.Audits)))
	router.Handle("GET /api/members", auth.Require(auth.PermissionRead)(listMembers(services.Team)))
	router.Handle("PUT /api/members/{id}/roles", auth.Require(auth.PermissionAdmin)(updateMemberRoles(services.Team, services.Audits)))
	router.Handle("GET /api/audit", auth.Require(auth.PermissionRead)(listAudit(services.Audits)))
	router.Handle("GET /api/alerts", auth.Require(auth.PermissionRead)(listAlerts(services.Alerts)))
	router.Handle("POST /api/alerts/{id}/acknowledge", auth.Require(auth.PermissionExecute)(acknowledgeAlert(services.Alerts, services.Audits)))
	router.Handle("POST /api/servers/{id}/credentials/rotate", auth.Require(auth.PermissionAdmin)(rotateCredential(services.Credentials, services.Audits)))
	router.Handle("POST /api/servers/{id}/credentials/revoke", auth.Require(auth.PermissionAdmin)(revokeCredentials(services.Credentials, services.Audits)))
	return router
}

func listSecrets(manager SecretManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "敏感参数服务尚未配置")
			return
		}
		items, err := manager.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取敏感参数失败")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"secrets": items})
	}
}

func createSecret(manager SecretManager, audits AuditManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "敏感参数服务尚未配置")
			return
		}
		var request struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := decodeJSON(w, r, &request); err != nil || strings.TrimSpace(request.Name) == "" || request.Value == "" {
			writeError(w, http.StatusBadRequest, "敏感参数名称和值不能为空")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		plaintext := []byte(request.Value)
		defer clear(plaintext)
		metadata, err := manager.Create(secret.WithCreator(r.Context(), principal.UserID), request.Name, plaintext)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "创建敏感参数失败")
			return
		}
		if !recordAudit(r, audits, principal.UserID, "secret.create", "secret", string(metadata.ID), map[string]any{"name": metadata.Name}) {
			writeError(w, http.StatusInternalServerError, "写入审计日志失败")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, metadata)
	}
}

func listMembers(manager TeamManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "团队服务尚未配置")
			return
		}
		members, err := manager.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取团队成员失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"members": members})
	}
}

func updateMemberRoles(manager TeamManager, audits AuditManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "团队服务尚未配置")
			return
		}
		var request struct {
			Roles []auth.RoleName `json:"roles"`
		}
		if err := decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, auth.ErrInvalidRoles.Error())
			return
		}
		member, err := manager.UpdateRoles(r.Context(), r.PathValue("id"), request.Roles)
		if errors.Is(err, auth.ErrInvalidRoles) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, auth.ErrMemberNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "更新成员角色失败")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		if !recordAudit(r, audits, principal.UserID, "member.roles.update", "user", member.ID, map[string]any{"roles": member.Roles}) {
			writeError(w, http.StatusInternalServerError, "写入审计日志失败")
			return
		}
		writeJSON(w, http.StatusOK, member)
	}
}

func listAudit(manager AuditManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "审计服务尚未配置")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		events, err := manager.List(r.Context(), audit.Filter{
			ActorID: r.URL.Query().Get("actorId"), Action: r.URL.Query().Get("action"),
			TargetType: r.URL.Query().Get("targetType"), Limit: limit,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取审计日志失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
	}
}

func listAlerts(manager AlertManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "告警服务尚未配置")
			return
		}
		items, err := manager.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取系统告警失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"alerts": items})
	}
}

func acknowledgeAlert(manager AlertManager, audits AuditManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "告警服务尚未配置")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		id := r.PathValue("id")
		if err := manager.Acknowledge(r.Context(), id, principal.UserID); err != nil {
			writeError(w, http.StatusInternalServerError, "确认告警失败")
			return
		}
		if !recordAudit(r, audits, principal.UserID, "alert.acknowledge", "alert", id, nil) {
			writeError(w, http.StatusInternalServerError, "写入审计日志失败")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func rotateCredential(manager CredentialManager, audits AuditManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "代理凭据服务尚未配置")
			return
		}
		serverID := r.PathValue("id")
		credentials, err := manager.Rotate(r.Context(), serverID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "轮换代理凭据失败")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		if !recordAudit(r, audits, principal.UserID, "server.credential.rotate", "server", serverID, nil) {
			writeError(w, http.StatusInternalServerError, "写入审计日志失败")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, credentials)
	}
}

func revokeCredentials(manager CredentialManager, audits AuditManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "代理凭据服务尚未配置")
			return
		}
		serverID := r.PathValue("id")
		if err := manager.Revoke(r.Context(), serverID); err != nil {
			writeError(w, http.StatusInternalServerError, "吊销代理凭据失败")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		if !recordAudit(r, audits, principal.UserID, "server.credential.revoke", "server", serverID, nil) {
			writeError(w, http.StatusInternalServerError, "写入审计日志失败")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func recordAudit(r *http.Request, manager AuditManager, actorID, action, targetType, targetID string, details map[string]any) bool {
	if manager == nil {
		return false
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	return manager.Record(r.Context(), audit.Event{
		ActorID: actorID, Action: action, TargetType: targetType, TargetID: targetID,
		Details: details, IPAddress: ip,
	}) == nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("请求正文只能包含一个 JSON 对象")
		}
		return err
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
