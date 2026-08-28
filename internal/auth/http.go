package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

const SessionCookieName = "yunling_session"

func Handler(service *Service) http.Handler {
	router := http.NewServeMux()
	router.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeAuthError(w, http.StatusBadRequest, "登录信息格式不正确")
			return
		}

		session, err := service.Login(r.Context(), request.Email, request.Password)
		if errors.Is(err, ErrInvalidCredentials) {
			writeAuthError(w, http.StatusUnauthorized, "账号或密码错误")
			return
		}
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "登录服务暂时不可用")
			return
		}

		http.SetCookie(w, sessionCookie(session.Token, session.ExpiresAt))
		writeJSON(w, http.StatusOK, map[string]any{
			"message":    "登录成功",
			"expires_at": session.ExpiresAt,
		})
	})

	router.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie(SessionCookieName)
		if cookie != nil {
			if err := service.Logout(r.Context(), cookie.Value); err != nil {
				writeAuthError(w, http.StatusInternalServerError, "退出登录失败，请稍后重试")
				return
			}
		}
		expired := sessionCookie("", time.Unix(0, 0))
		expired.MaxAge = -1
		http.SetCookie(w, expired)
		w.WriteHeader(http.StatusNoContent)
	})

	router.HandleFunc("GET /api/auth/session", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		principal, err := service.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"user": principal})
	})

	return router
}

func sessionCookie(token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
