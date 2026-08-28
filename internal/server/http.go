package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	sync        SyncCoordinator
	artifacts   AgentArtifactProvider
	runEvents   RunEventReceiver
	logs        LogReceiver
	reconciler  RunningReconciler
}

type SyncCoordinator interface {
	NextCommand(context.Context, string) (agentprotocol.SyncCommand, bool, error)
	RecordResult(context.Context, string, agentprotocol.SyncResult) error
}

type AgentArtifactProvider interface {
	OpenVersionArtifact(context.Context, string, string) (io.ReadCloser, string, error)
}

type RunEventReceiver interface {
	Apply(context.Context, agentprotocol.RunEvent) error
}

type LogReceiver interface {
	Accept(context.Context, agentprotocol.LogChunk) (uint64, error)
}

type RunningReconciler interface {
	Reconcile(context.Context, agentprotocol.RunningReport) error
}

func WithConnectionHub(connections *AgentConnectionHub) HandlerOption {
	return func(options *handlerOptions) {
		options.connections = connections
	}
}

func WithSyncCoordinator(coordinator SyncCoordinator) HandlerOption {
	return func(options *handlerOptions) { options.sync = coordinator }
}

func WithAgentArtifactProvider(provider AgentArtifactProvider) HandlerOption {
	return func(options *handlerOptions) { options.artifacts = provider }
}

func WithRunEventReceiver(receiver RunEventReceiver) HandlerOption {
	return func(options *handlerOptions) { options.runEvents = receiver }
}

func WithLogReceiver(receiver LogReceiver) HandlerOption {
	return func(options *handlerOptions) { options.logs = receiver }
}

func WithRunningReconciler(reconciler RunningReconciler) HandlerOption {
	return func(options *handlerOptions) { options.reconciler = reconciler }
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
	if configuration.artifacts != nil {
		router.HandleFunc("GET /api/agent/scripts/{versionID}/artifact", agentArtifactHandler(enrollment, configuration.artifacts))
	}
	router.HandleFunc("GET /api/agent/connect", agentConnectHandler(registry, enrollment, configuration))
	return router
}

func agentArtifactHandler(enrollment EnrollmentManager, provider AgentArtifactProvider) http.HandlerFunc {
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
		body, checksum, err := provider.OpenVersionArtifact(r.Context(), serverID, r.PathValue("versionID"))
		if err != nil {
			writeServerError(w, http.StatusNotFound, "脚本包不存在")
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("X-Content-SHA256", checksum)
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, body)
	}
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
	configuration handlerOptions,
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
		unregister := configuration.connections.Register(serverID, connection)
		defer unregister()
		// 日志正文最大为 64 KiB；JSON 转义控制字符时消息可能膨胀到约 6 倍。
		// 传输层保留 512 KiB 上限，业务层仍按日志块大小严格校验。
		connection.SetReadLimit(512 << 10)
		ctx := context.Background()
		for {
			var payload json.RawMessage
			if err := wsjson.Read(ctx, connection, &payload); err != nil {
				return
			}
			var messageType struct {
				MessageType string                  `json:"message_type"`
				RunID       string                  `json:"run_id"`
				Stream      string                  `json:"stream"`
				Type        string                  `json:"type"`
				State       agentprotocol.SyncState `json:"state"`
			}
			if err := json.Unmarshal(payload, &messageType); err != nil {
				_ = connection.Close(websocket.StatusUnsupportedData, "代理消息格式无效")
				return
			}
			switch {
			case messageType.MessageType == "running_report":
				if configuration.reconciler == nil {
					_ = connection.Close(websocket.StatusPolicyViolation, "状态对账服务尚未启用")
					return
				}
				var report agentprotocol.RunningReport
				if err := json.Unmarshal(payload, &report); err != nil {
					_ = connection.Close(websocket.StatusUnsupportedData, "运行状态报告无效")
					return
				}
				report.ServerID = serverID
				if err := configuration.reconciler.Reconcile(ctx, report); err != nil {
					_ = connection.Close(websocket.StatusPolicyViolation, "运行状态对账失败")
					return
				}
			case messageType.MessageType == "log_chunk" || messageType.Stream != "":
				if configuration.logs == nil {
					_ = connection.Close(websocket.StatusPolicyViolation, "日志接收服务尚未启用")
					return
				}
				var chunk agentprotocol.LogChunk
				if err := json.Unmarshal(payload, &chunk); err != nil {
					_ = connection.Close(websocket.StatusUnsupportedData, "日志块格式无效")
					return
				}
				next, err := configuration.logs.Accept(ctx, chunk)
				if err != nil {
					_ = connection.Close(websocket.StatusPolicyViolation, "日志块序号无效")
					return
				}
				if err := configuration.connections.Write(ctx, connection, agentprotocol.LogAcknowledgement{
					MessageType: "log_ack", RunID: chunk.RunID, ExecutionToken: chunk.ExecutionToken,
					Stream: chunk.Stream, NextSequence: next,
				}); err != nil {
					return
				}
			case messageType.RunID != "" && messageType.Type != "":
				if configuration.runEvents == nil {
					_ = connection.Close(websocket.StatusPolicyViolation, "运行事件服务尚未启用")
					return
				}
				var event agentprotocol.RunEvent
				if err := json.Unmarshal(payload, &event); err != nil || configuration.runEvents.Apply(ctx, event) != nil {
					_ = connection.Close(websocket.StatusPolicyViolation, "任务运行事件无效")
					return
				}
			case messageType.State != "":
				if configuration.sync == nil {
					_ = connection.Close(websocket.StatusPolicyViolation, "同步服务尚未启用")
					return
				}
				var result agentprotocol.SyncResult
				if err := json.Unmarshal(payload, &result); err != nil || configuration.sync.RecordResult(ctx, serverID, result) != nil {
					_ = connection.Close(websocket.StatusPolicyViolation, "脚本同步结果无效")
					return
				}
			default:
				var heartbeat agentprotocol.Heartbeat
				if err := json.Unmarshal(payload, &heartbeat); err != nil {
					_ = connection.Close(websocket.StatusUnsupportedData, "心跳格式无效")
					return
				}
				heartbeat.ServerID = serverID
				if err := registry.AcceptHeartbeat(ctx, heartbeat); err != nil {
					_ = connection.Close(websocket.StatusPolicyViolation, "心跳内容无效")
					return
				}
			}
			if configuration.sync != nil {
				command, ok, err := configuration.sync.NextCommand(ctx, serverID)
				if err != nil {
					_ = connection.Close(websocket.StatusInternalError, "同步调度暂时不可用")
					return
				}
				if ok {
					if err := configuration.connections.Write(ctx, connection, command); err != nil {
						return
					}
				}
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
