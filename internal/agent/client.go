package agent

import (
	"context"
	"encoding/json"
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

func WithInitialHeartbeatSequence(sequence uint64) ClientOption {
	return func(client *Client) {
		client.sequence = sequence
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
	readOnce   sync.Once
	readMu     sync.Mutex
	readErr    error
	readCtx    context.Context
	cancelRead context.CancelFunc
	readDone   chan struct{}
	syncQueue  chan agentprotocol.SyncCommand
	execQueue  chan agentprotocol.ExecutionCommand
	logAcks    chan agentprotocol.LogAcknowledgement
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
	readCtx, cancelRead := context.WithCancel(context.Background())
	return &WebSocketSender{
		connection: connection,
		readCtx:    readCtx,
		cancelRead: cancelRead,
		readDone:   make(chan struct{}),
		syncQueue:  make(chan agentprotocol.SyncCommand, 32),
		execQueue:  make(chan agentprotocol.ExecutionCommand, 32),
		logAcks:    make(chan agentprotocol.LogAcknowledgement, 32),
	}, nil
}

func (s *WebSocketSender) SendHeartbeat(ctx context.Context, heartbeat agentprotocol.Heartbeat) error {
	return s.write(ctx, heartbeat)
}

func (s *WebSocketSender) ReceiveSyncCommand(ctx context.Context) (agentprotocol.SyncCommand, error) {
	s.startReader()
	return receiveCommand(ctx, s.syncQueue, s.readDone, s.readerError)
}

func (s *WebSocketSender) ReceiveExecutionCommand(ctx context.Context) (agentprotocol.ExecutionCommand, error) {
	s.startReader()
	return receiveCommand(ctx, s.execQueue, s.readDone, s.readerError)
}

func (s *WebSocketSender) SendSyncResult(ctx context.Context, result agentprotocol.SyncResult) error {
	return s.write(ctx, result)
}

func (s *WebSocketSender) SendRunEvent(ctx context.Context, event agentprotocol.RunEvent) error {
	return s.write(ctx, event)
}

func (s *WebSocketSender) SendRunningReport(ctx context.Context, report agentprotocol.RunningReport) error {
	report.MessageType = "running_report"
	return s.write(ctx, report)
}

func (s *WebSocketSender) SendLogChunk(ctx context.Context, chunk agentprotocol.LogChunk) (uint64, error) {
	chunk.MessageType = "log_chunk"
	s.startReader()
	if err := s.write(ctx, chunk); err != nil {
		return 0, err
	}
	acknowledgement, err := receiveCommand(ctx, s.logAcks, s.readDone, s.readerError)
	if err != nil {
		return 0, err
	}
	if acknowledgement.RunID != chunk.RunID || acknowledgement.ExecutionToken != chunk.ExecutionToken || acknowledgement.Stream != chunk.Stream {
		return 0, fmt.Errorf("中央日志确认与上传日志流不匹配")
	}
	return acknowledgement.NextSequence, nil
}

func (s *WebSocketSender) write(ctx context.Context, value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return wsjson.Write(ctx, s.connection, value)
}

func (s *WebSocketSender) Close() error {
	s.cancelRead()
	return s.connection.Close(websocket.StatusNormalClosure, "代理停止")
}

func (s *WebSocketSender) startReader() {
	s.readOnce.Do(func() { go s.readCommands() })
}

func (s *WebSocketSender) readCommands() {
	defer close(s.readDone)
	for {
		var payload json.RawMessage
		if err := wsjson.Read(s.readCtx, s.connection, &payload); err != nil {
			s.setReaderError(err)
			return
		}
		var header struct {
			MessageType string                             `json:"message_type"`
			Type        agentprotocol.ExecutionCommandType `json:"type"`
		}
		if err := json.Unmarshal(payload, &header); err != nil {
			s.setReaderError(fmt.Errorf("解析中央命令类型：%w", err))
			return
		}
		if header.MessageType == "log_ack" {
			var acknowledgement agentprotocol.LogAcknowledgement
			if err := json.Unmarshal(payload, &acknowledgement); err != nil || acknowledgement.NextSequence == 0 {
				s.setReaderError(fmt.Errorf("中央日志确认格式无效"))
				return
			}
			select {
			case s.logAcks <- acknowledgement:
			case <-s.readCtx.Done():
				return
			}
			continue
		}
		if header.Type != "" {
			var command agentprotocol.ExecutionCommand
			if err := json.Unmarshal(payload, &command); err != nil || (command.Type != agentprotocol.CommandAssign && command.Type != agentprotocol.CommandCancel) {
				s.setReaderError(fmt.Errorf("中央执行命令格式无效"))
				return
			}
			select {
			case s.execQueue <- command:
			case <-s.readCtx.Done():
				return
			}
			continue
		}
		var command agentprotocol.SyncCommand
		if err := json.Unmarshal(payload, &command); err != nil {
			s.setReaderError(fmt.Errorf("中央同步命令格式无效：%w", err))
			return
		}
		select {
		case s.syncQueue <- command:
		case <-s.readCtx.Done():
			return
		}
	}
}

func (s *WebSocketSender) setReaderError(err error) {
	s.readMu.Lock()
	s.readErr = err
	s.readMu.Unlock()
}

func (s *WebSocketSender) readerError() error {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	return s.readErr
}

func receiveCommand[T any](ctx context.Context, queue <-chan T, done <-chan struct{}, readError func() error) (T, error) {
	var zero T
	select {
	case command := <-queue:
		return command, nil
	default:
	}
	select {
	case command := <-queue:
		return command, nil
	case <-done:
		select {
		case command := <-queue:
			return command, nil
		default:
		}
		if err := readError(); err != nil {
			return zero, err
		}
		return zero, fmt.Errorf("中央命令连接已关闭")
	case <-ctx.Done():
		return zero, ctx.Err()
	}
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
