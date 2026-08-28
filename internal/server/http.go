package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/auth"
)

type EnrollmentManager interface {
	CreateToken(ctx context.Context, input EnrollmentTokenInput) (EnrollmentToken, error)
	Enroll(ctx context.Context, token string) (AgentCredentials, error)
	Authenticate(ctx context.Context, credential string) (string, error)
}

type HandlerOption func(*handlerOptions)

type handlerOptions struct {
	connections *AgentConnectionHub
}

func WithConnectionHub(connections *AgentConnectionHub) HandlerOption {
	return func(options *handlerOptions) {
		options.connections = connections
	}
}

func Handler(registry *Registry, enrollment EnrollmentManager, options ...HandlerOption) http.Handler {
	configuration := handlerOptions{connections: NewAgentConnectionHub()}
	for _, option := range options {
		option(&configuration)
	}
	router := http.NewServeMux()
	router.Handle(
		"POST /api/servers/enrollment-tokens",
		auth.Require(auth.PermissionAdmin)(createEnrollmentTokenHandler(enrollment)),
	)
	router.HandleFunc("POST /api/agent/enroll", enrollAgentHandler(enrollment))
	router.HandleFunc("GET /api/agent/connect", agentConnectHandler(registry, enrollment, configuration.connections))
	return router
}

func ManagementHandler(query ManagementQuery) http.Handler {
	router := http.NewServeMux()
	router.Handle("GET /api/dashboard", auth.Require(auth.PermissionRead)(dashboardHandler(query)))
	router.Handle("GET /api/servers", auth.Require(auth.PermissionRead)(listServersHandler(query)))
	router.Handle("PATCH /api/servers/{id}", auth.Require(auth.PermissionExecute)(updateServerHandler(query)))
	return router
}

func dashboardHandler(query ManagementQuery) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dashboard, err := query.Dashboard(r.Context())
		if err != nil {
			writeServerError(w, http.StatusInternalServerError, "读取运行总览失败")
			return
		}
		if dashboard.Servers == nil {
			dashboard.Servers = []ServerView{}
		}
		if dashboard.RecentEvents == nil {
			dashboard.RecentEvents = []RecentEvent{}
		}
		writeServerJSON(w, http.StatusOK, dashboard)
	})
}

func listServersHandler(query ManagementQuery) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		servers, err := query.ListServers(r.Context())
		if err != nil {
			writeServerError(w, http.StatusInternalServerError, "读取服务器列表失败")
			return
		}
		if servers == nil {
			servers = []ServerView{}
		}
		writeServerJSON(w, http.StatusOK, map[string]any{"servers": servers})
	})
}

func updateServerHandler(query ManagementQuery) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input UpdateServerInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeServerError(w, http.StatusBadRequest, ErrInvalidServerUpdate.Error())
			return
		}
		updated, err := query.UpdateServer(r.Context(), r.PathValue("id"), input)
		if errors.Is(err, ErrInvalidServerUpdate) {
			writeServerError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, ErrServerNotFound) {
			writeServerError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeServerError(w, http.StatusInternalServerError, "更新服务器失败")
			return
		}
		writeServerJSON(w, http.StatusOK, updated)
	})
}

func createEnrollmentTokenHandler(enrollment EnrollmentManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Name          string            `json:"name"`
			CloudProvider string            `json:"cloud_provider"`
			Region        string            `json:"region"`
			Labels        map[string]string `json:"labels"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || strings.TrimSpace(request.Name) == "" {
			writeServerError(w, http.StatusBadRequest, "服务器注册信息格式不正确")
			return
		}
		issued, err := enrollment.CreateToken(r.Context(), EnrollmentTokenInput{
			Name:          strings.TrimSpace(request.Name),
			CloudProvider: strings.TrimSpace(request.CloudProvider),
			Region:        strings.TrimSpace(request.Region),
			Labels:        request.Labels,
		})
		if err != nil {
			writeServerError(w, http.StatusInternalServerError, "创建服务器注册令牌失败")
			return
		}
		writeServerJSON(w, http.StatusCreated, map[string]any{
			"id":         issued.ID,
			"token":      issued.Token,
			"expires_at": issued.ExpiresAt,
		})
	})
}

func enrollAgentHandler(enrollment EnrollmentManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Token string `json:"token"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.Token == "" {
			writeServerError(w, http.StatusBadRequest, "注册令牌不能为空")
			return
		}
		credentials, err := enrollment.Enroll(r.Context(), request.Token)
		if errors.Is(err, ErrEnrollmentTokenInvalid) {
			writeServerError(w, http.StatusUnauthorized, ErrEnrollmentTokenInvalid.Error())
			return
		}
		if err != nil {
			writeServerError(w, http.StatusInternalServerError, "代理注册服务暂时不可用")
			return
		}
		writeServerJSON(w, http.StatusCreated, credentials)
	}
}

func agentConnectHandler(
	registry *Registry,
	enrollment EnrollmentManager,
	connections *AgentConnectionHub,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		credential, ok := bearerCredential(r.Header.Get("Authorization"))
		if !ok {
			writeServerError(w, http.StatusUnauthorized, "缺少代理凭据")
			return
		}
		serverID, err := enrollment.Authenticate(r.Context(), credential)
		if err != nil {
			writeServerError(w, http.StatusUnauthorized, "代理凭据无效或已撤销")
			return
		}

		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		unregister := connections.Register(serverID, connection)
		defer unregister()
		connection.SetReadLimit(64 << 10)
		ctx := context.Background()
		for {
			var heartbeat agentprotocol.Heartbeat
			if err := wsjson.Read(ctx, connection, &heartbeat); err != nil {
				return
			}
			heartbeat.ServerID = serverID
			if err := registry.AcceptHeartbeat(ctx, heartbeat); err != nil {
				_ = connection.Close(websocket.StatusPolicyViolation, "心跳内容无效")
				return
			}
		}
	}
}

func bearerCredential(value string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	credential := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	return credential, credential != ""
}

func writeServerError(w http.ResponseWriter, status int, message string) {
	writeServerJSON(w, status, map[string]string{"message": message})
}

func writeServerJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
