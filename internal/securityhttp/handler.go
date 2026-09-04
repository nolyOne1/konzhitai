package securityhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/mail"
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
	List(context.Context, auth.MemberStatus) ([]auth.Member, error)
	Create(context.Context, string, auth.CreateMemberInput) (auth.CreateMemberResult, error)
	UpdateRoles(context.Context, string, string, []auth.RoleName) (auth.Member, error)
	SetEnabled(context.Context, string, string, bool) (auth.Member, error)
	Remove(context.Context, string, string) (auth.Member, error)
	Restore(context.Context, string, string) (auth.Member, error)
	ResetPassword(context.Context, string, string) (auth.ResetPasswordResult, error)
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
	router.Handle("POST /api/members", auth.Require(auth.PermissionAdmin)(createMember(services.Team)))
	router.Handle("PUT /api/members/{id}/roles", auth.Require(auth.PermissionAdmin)(updateMemberRoles(services.Team)))
	router.Handle("POST /api/members/{id}/enable", auth.Require(auth.PermissionAdmin)(setMemberEnabled(services.Team, true)))
	router.Handle("POST /api/members/{id}/disable", auth.Require(auth.PermissionAdmin)(setMemberEnabled(services.Team, false)))
	router.Handle("DELETE /api/members/{id}", auth.Require(auth.PermissionAdmin)(removeMember(services.Team)))
	router.Handle("POST /api/members/{id}/restore", auth.Require(auth.PermissionAdmin)(restoreMember(services.Team)))
	router.Handle("POST /api/members/{id}/password/reset", auth.Require(auth.PermissionAdmin)(resetMemberPassword(services.Team)))
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
		status := auth.MemberStatus(r.URL.Query().Get("status"))
		if status == "" {
			status = auth.MemberStatusAll
		}
		if status != auth.MemberStatusActive && status != auth.MemberStatusDisabled && status != auth.MemberStatusRemoved && status != auth.MemberStatusAll {
			writeError(w, http.StatusBadRequest, auth.ErrInvalidMember.Error())
			return
		}
		members, err := manager.List(r.Context(), status)
		if err != nil {
			if writeMemberError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "读取团队成员失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"members": members})
	}
}

func createMember(manager TeamManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "团队服务尚未配置")
			return
		}
		var request auth.CreateMemberInput
		if err := decodeJSON(w, r, &request); err != nil || !validCreateMemberInput(request) {
			writeError(w, http.StatusBadRequest, auth.ErrInvalidMember.Error())
			return
		}
		result, err := manager.Create(r.Context(), memberActorID(r), request)
		if err != nil {
			if writeMemberError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "创建成员失败")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, result)
	}
}

func updateMemberRoles(manager TeamManager) http.HandlerFunc {
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
		member, err := manager.UpdateRoles(r.Context(), memberActorID(r), r.PathValue("id"), request.Roles)
		if err != nil {
			if writeMemberError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "更新成员角色失败")
			return
		}
		writeJSON(w, http.StatusOK, member)
	}
}

func setMemberEnabled(manager TeamManager, enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "团队服务尚未配置")
			return
		}
		member, err := manager.SetEnabled(r.Context(), memberActorID(r), r.PathValue("id"), enabled)
		if err != nil {
			if writeMemberError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "更新成员状态失败")
			return
		}
		writeJSON(w, http.StatusOK, member)
	}
}

func removeMember(manager TeamManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "团队服务尚未配置")
			return
		}
		if _, err := manager.Remove(r.Context(), memberActorID(r), r.PathValue("id")); err != nil {
			if writeMemberError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "移除成员失败")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func restoreMember(manager TeamManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "团队服务尚未配置")
			return
		}
		member, err := manager.Restore(r.Context(), memberActorID(r), r.PathValue("id"))
		if err != nil {
			if writeMemberError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "恢复成员失败")
			return
		}
		writeJSON(w, http.StatusOK, member)
	}
}

func resetMemberPassword(manager TeamManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, "团队服务尚未配置")
			return
		}
		result, err := manager.ResetPassword(r.Context(), memberActorID(r), r.PathValue("id"))
		if err != nil {
			if writeMemberError(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "重置成员密码失败")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, result)
	}
}

func memberActorID(r *http.Request) string {
	principal, _ := auth.PrincipalFromContext(r.Context())
	return principal.UserID
}

func validCreateMemberInput(input auth.CreateMemberInput) bool {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	parsed, err := mail.ParseAddress(email)
	return err == nil && parsed.Address == email && strings.TrimSpace(input.DisplayName) != "" && len(input.Roles) > 0
}

func writeMemberError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, auth.ErrInvalidMember), errors.Is(err, auth.ErrInvalidRoles):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrMemberNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, auth.ErrDuplicateEmail), errors.Is(err, auth.ErrMemberStateConflict), errors.Is(err, auth.ErrCannotModifySelf), errors.Is(err, auth.ErrLastAdmin):
		writeError(w, http.StatusConflict, err.Error())
	default:
		return false
	}
	return true
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
