package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"yunling.local/platform/internal/agentprotocol"
)

const HeartbeatInterval = 5 * time.Second

type Snapshotter interface {
	Snapshot(ctx context.Context) (agentprotocol.Heartbeat, error)
}

type HeartbeatSender interface {
	SendHeartbeat(ctx context.Context, heartbeat agentprotocol.Heartbeat) error
}

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type TickerFactory func(interval time.Duration) Ticker

type ClientOption func(*Client)

func WithTickerFactory(factory TickerFactory) ClientOption {
	return func(client *Client) {
		client.newTicker = factory
	}
}

type Client struct {
	serverID  string
	version   string
	collector Snapshotter
	sender    HeartbeatSender
	newTicker TickerFactory
	sequence  uint64
}

func NewClient(
	serverID string,
	version string,
	collector Snapshotter,
	sender HeartbeatSender,
	options ...ClientOption,
) *Client {
	client := &Client{
		serverID:  serverID,
		version:   version,
		collector: collector,
		sender:    sender,
		newTicker: func(interval time.Duration) Ticker { return realTicker{Ticker: time.NewTicker(interval)} },
	}
	for _, option := range options {
		option(client)
	}
	return client
}

func (c *Client) Run(ctx context.Context) error {
	ticker := c.newTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case sentAt := <-ticker.C():
			heartbeat, err := c.collector.Snapshot(ctx)
			if err != nil {
				return fmt.Errorf("采集代理心跳：%w", err)
			}
			c.sequence++
			heartbeat.ServerID = c.serverID
			heartbeat.Sequence = c.sequence
			heartbeat.SentAt = sentAt.UTC()
			heartbeat.AgentVersion = c.version
			if err := c.sender.SendHeartbeat(ctx, heartbeat); err != nil {
				return fmt.Errorf("发送代理心跳：%w", err)
			}
		}
	}
}

type realTicker struct {
	*time.Ticker
}

func (t realTicker) C() <-chan time.Time {
	return t.Ticker.C
}

type WebSocketSender struct {
	connection *websocket.Conn
	writeMu    sync.Mutex
}

func DialHeartbeatSender(ctx context.Context, controlURL, credential string) (*WebSocketSender, error) {
	endpoint, err := agentConnectURL(controlURL)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+credential)
	connection, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		return nil, fmt.Errorf("连接中央服务 WebSocket（状态码 %d）：%w", status, err)
	}
	return &WebSocketSender{connection: connection}, nil
}

func (s *WebSocketSender) SendHeartbeat(ctx context.Context, heartbeat agentprotocol.Heartbeat) error {
	return s.write(ctx, heartbeat)
}

func (s *WebSocketSender) ReceiveSyncCommand(ctx context.Context) (agentprotocol.SyncCommand, error) {
	var command agentprotocol.SyncCommand
	err := wsjson.Read(ctx, s.connection, &command)
	return command, err
}

func (s *WebSocketSender) SendSyncResult(ctx context.Context, result agentprotocol.SyncResult) error {
	return s.write(ctx, result)
}

func (s *WebSocketSender) write(ctx context.Context, value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return wsjson.Write(ctx, s.connection, value)
}

func (s *WebSocketSender) Close() error {
	return s.connection.Close(websocket.StatusNormalClosure, "代理停止")
}

func agentConnectURL(controlURL string) (string, error) {
	parsed, err := url.Parse(controlURL)
	if err != nil {
		return "", fmt.Errorf("解析中央服务地址：%w", err)
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		if !isLoopbackHostname(parsed.Hostname()) {
			return "", fmt.Errorf("中央服务地址必须使用 HTTPS")
		}
		parsed.Scheme = "ws"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("中央服务地址必须使用 HTTPS 或 HTTP")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("中央服务地址缺少主机名")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/agent/connect"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func isLoopbackHostname(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	address := net.ParseIP(hostname)
	return address != nil && address.IsLoopback()
}
