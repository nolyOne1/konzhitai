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
	"strings"

	"yunling.local/platform/internal/auth"
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

type Services struct {
	Notifications NotificationManager
	Deliveries    DeliveryManager
}

func NewHandler(services Services, allowedOrigin string) http.Handler {
	origin, originValid := normalizeOrigin(allowedOrigin)
	router := http.NewServeMux()
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
