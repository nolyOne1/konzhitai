package auth

import (
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
)

func PasswordHandler(service *PasswordChangeService, allowedOrigin string) http.Handler {
	origin, originValid := passwordAllowedOrigin(allowedOrigin)
	router := http.NewServeMux()
	router.HandleFunc("POST /api/auth/password", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || strings.TrimSpace(principal.UserID) == "" {
			writeAuthError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || cookie.Value == "" {
			writeAuthError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
			return
		}
		if !originValid {
			writeAuthError(w, http.StatusServiceUnavailable, "改密服务来源配置无效")
			return
		}
		if r.Header.Get("Origin") != origin {
			writeAuthError(w, http.StatusForbidden, "请求来源不受信任")
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeAuthError(w, http.StatusUnsupportedMediaType, "请求必须使用 JSON")
			return
		}
		var request struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeAuthError(w, http.StatusBadRequest, "改密信息格式不正确")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeAuthError(w, http.StatusBadRequest, "改密信息格式不正确")
			return
		}
		ipAddress := passwordSourceIP(r)
		if err := service.Change(
			r.Context(), principal, cookie.Value,
			request.CurrentPassword, request.NewPassword, ipAddress,
		); err != nil {
			switch {
			case errors.Is(err, ErrPasswordRateLimited):
				writeAuthError(w, http.StatusTooManyRequests, ErrPasswordRateLimited.Error())
			case errors.Is(err, ErrPasswordRejected), errors.Is(err, ErrPasswordChanged):
				writeAuthError(w, http.StatusBadRequest, ErrPasswordRejected.Error())
			default:
				writeAuthError(w, http.StatusInternalServerError, "改密服务暂时不可用")
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return router
}

func passwordAllowedOrigin(value string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", false
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", false
	}
	return parsed.Scheme + "://" + parsed.Host, true
}

func passwordSourceIP(r *http.Request) string {
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
	remote := strings.TrimSpace(r.RemoteAddr)
	if net.ParseIP(remote) != nil {
		return remote
	}
	return ""
}
