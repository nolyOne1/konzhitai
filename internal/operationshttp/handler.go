package operationshttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/backup"
	"yunling.local/platform/internal/notification"
)

type NotificationManager interface {
	Get(context.Context) (notification.FeishuConfigView, error)
	Update(context.Context, string, string, notification.FeishuConfigInput) (notification.FeishuConfigView, error)
}

type DeliveryManager interface {
	EnqueueTest(context.Context, string) (notification.Delivery, error)
	GetDelivery(context.Context, string) (notification.Delivery, error)
}

type BackupManager interface {
	Summary(context.Context) (backup.Summary, error)
	ListBackups(context.Context, int) ([]backup.BackupRun, error)
	RequestBackup(context.Context, string, string, time.Time) (backup.BackupRun, error)
	ListVerifications(context.Context, int) ([]backup.RestoreVerification, error)
	RequestVerification(context.Context, string, string, string, time.Time) (backup.RestoreVerification, error)
}

type Services struct {
	Notifications NotificationManager
	Deliveries    DeliveryManager
	Backups       BackupManager
}

func NewHandler(services Services, allowedOrigin string) http.Handler {
	origin, originValid := normalizeOrigin(allowedOrigin)
	router := http.NewServeMux()
	router.Handle("GET /api/operations/summary", auth.Require(auth.PermissionRead)(getBackupSummary(services.Backups)))
	router.Handle("GET /api/operations/backups", auth.Require(auth.PermissionRead)(listBackups(services.Backups)))
	router.Handle("POST /api/operations/backups", auth.Require(auth.PermissionAdmin)(requestBackup(services.Backups, origin, originValid)))
	router.Handle("GET /api/operations/verifications", auth.Require(auth.PermissionRead)(listVerifications(services.Backups)))
	router.Handle("POST /api/operations/verifications", auth.Require(auth.PermissionAdmin)(requestVerification(services.Backups, origin, originValid)))
	router.Handle(
		"GET /api/operations/notifications/feishu",
		auth.Require(auth.PermissionRead)(getFeishuConfig(services.Notifications)),
	)
	router.Handle(
		"PUT /api/operations/notifications/feishu",
		auth.Require(auth.PermissionAdmin)(putFeishuConfig(services.Notifications, origin, originValid)),
	)
	router.Handle(
		"POST /api/operations/notifications/feishu/test",
		auth.Require(auth.PermissionAdmin)(enqueueFeishuTest(services.Deliveries, origin, originValid)),
	)
	router.Handle(
		"GET /api/operations/notifications/{id}",
		auth.Require(auth.PermissionRead)(getDelivery(services.Deliveries)),
	)
	return router
}

func getBackupSummary(manager BackupManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if manager == nil {
			writeJSON(w, http.StatusOK, backup.Summary{Status: "unavailable"})
			return
		}
		summary, err := manager.Summary(r.Context())
		if errors.Is(err, backup.ErrUnavailable) {
			writeJSON(w, http.StatusOK, backup.Summary{Status: "unavailable"})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取备份运行摘要失败")
			return
		}
		writeJSON(w, http.StatusOK, summary)
	}
}

func listBackups(manager BackupManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, backup.ErrUnavailable.Error())
			return
		}
		items, err := manager.ListBackups(r.Context(), 100)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取备份历史失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"backups": items})
	}
}

func requestBackup(manager BackupManager, origin string, originValid bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		key, ok := validateOperationWrite(w, r, origin, originValid, true)
		if !ok {
			return
		}
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, backup.ErrUnavailable.Error())
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		run, err := manager.RequestBackup(r.Context(), principal.UserID, key, time.Now().UTC())
		if errors.Is(err, backup.ErrInvalidRequest) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "创建备份请求失败")
			return
		}
		writeJSON(w, http.StatusAccepted, run)
	}
}

func listVerifications(manager BackupManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, backup.ErrUnavailable.Error())
			return
		}
		items, err := manager.ListVerifications(r.Context(), 100)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取恢复校验历史失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"verifications": items})
	}
}

func requestVerification(manager BackupManager, origin string, originValid bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		key, ok := validateOperationWrite(w, r, origin, originValid, false)
		if !ok {
			return
		}
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, backup.ErrUnavailable.Error())
			return
		}
		var input struct {
			BackupRunID string `json:"backupRunId"`
		}
		if !decodeExactJSON(w, r, &input, 2048) {
			return
		}
		if _, err := uuid.Parse(input.BackupRunID); err != nil {
			writeError(w, http.StatusBadRequest, "备份记录编号无效")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		verification, err := manager.RequestVerification(r.Context(), principal.UserID, input.BackupRunID, key, time.Now().UTC())
		if errors.Is(err, backup.ErrInvalidRequest) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "创建恢复校验请求失败")
			return
		}
		writeJSON(w, http.StatusAccepted, verification)
	}
}

func validateOperationWrite(w http.ResponseWriter, r *http.Request, origin string, originValid, decodeEmpty bool) (string, bool) {
	if !originValid {
		writeError(w, http.StatusServiceUnavailable, "运维服务来源配置无效")
		return "", false
	}
	if r.Header.Get("Origin") != origin {
		writeError(w, http.StatusForbidden, "请求来源不受信任")
		return "", false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "请求必须使用 JSON")
		return "", false
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if _, err := uuid.Parse(key); err != nil {
		writeError(w, http.StatusBadRequest, "Idempotency-Key 必须是 UUID")
		return "", false
	}
	if decodeEmpty {
		var body struct{}
		if !decodeExactJSON(w, r, &body, 1024) {
			return "", false
		}
	}
	return key, true
}

func decodeExactJSON(w http.ResponseWriter, r *http.Request, destination any, maximum int64) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maximum))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return false
	}
	return true
}

func getFeishuConfig(manager NotificationManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, notification.ErrUnavailable.Error())
			return
		}
		view, err := manager.Get(r.Context())
		if errors.Is(err, notification.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取飞书通知配置失败")
			return
		}
		writeJSON(w, http.StatusOK, view)
	}
}

func putFeishuConfig(manager NotificationManager, origin string, originValid bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, notification.ErrUnavailable.Error())
			return
		}
		if !originValid {
			writeError(w, http.StatusServiceUnavailable, "通知服务来源配置无效")
			return
		}
		if r.Header.Get("Origin") != origin {
			writeError(w, http.StatusForbidden, "请求来源不受信任")
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "请求必须使用 JSON")
			return
		}
		var input notification.FeishuConfigInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, notification.ErrInvalidConfig.Error())
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, notification.ErrInvalidConfig.Error())
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		view, err := manager.Update(r.Context(), principal.UserID, sourceIP(r), input)
		if errors.Is(err, notification.ErrInvalidConfig) || errors.Is(err, notification.ErrInvalidWebhook) {
			writeError(w, http.StatusBadRequest, notification.ErrInvalidConfig.Error())
			return
		}
		if errors.Is(err, notification.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "保存飞书通知配置失败")
			return
		}
		writeJSON(w, http.StatusOK, view)
	}
}

func enqueueFeishuTest(manager DeliveryManager, origin string, originValid bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, notification.ErrUnavailable.Error())
			return
		}
		if !originValid {
			writeError(w, http.StatusServiceUnavailable, "通知服务来源配置无效")
			return
		}
		if r.Header.Get("Origin") != origin {
			writeError(w, http.StatusForbidden, "请求来源不受信任")
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "请求必须使用 JSON")
			return
		}
		var body struct{}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "测试消息请求格式不正确")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "测试消息请求格式不正确")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		delivery, err := manager.EnqueueTest(r.Context(), principal.UserID)
		if errors.Is(err, notification.ErrNotConfigured) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "创建飞书测试消息失败")
			return
		}
		writeJSON(w, http.StatusAccepted, delivery)
	}
}

func getDelivery(manager DeliveryManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if manager == nil {
			writeError(w, http.StatusServiceUnavailable, notification.ErrUnavailable.Error())
			return
		}
		delivery, err := manager.GetDelivery(r.Context(), r.PathValue("id"))
		if errors.Is(err, notification.ErrDeliveryNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "读取通知发送状态失败")
			return
		}
		writeJSON(w, http.StatusOK, delivery)
	}
}

func normalizeOrigin(value string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", false
	}
	return parsed.Scheme + "://" + parsed.Host, true
}

func sourceIP(r *http.Request) string {
	trustProxy, _ := strconv.ParseBool(os.Getenv("YUNLING_TRUST_PROXY"))
	if trustProxy {
		forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
		if !strings.Contains(forwarded, ",") && net.ParseIP(forwarded) != nil {
			return forwarded
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	if parsed := net.ParseIP(strings.TrimSpace(r.RemoteAddr)); parsed != nil {
		return parsed.String()
	}
	return ""
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
