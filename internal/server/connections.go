package server

import (
	"sync"

	"github.com/coder/websocket"
)

type AgentConnectionHub struct {
	mu          sync.Mutex
	connections map[string]map[*websocket.Conn]struct{}
	disabled    map[string]struct{}
}

func NewAgentConnectionHub() *AgentConnectionHub {
	return &AgentConnectionHub{
		connections: make(map[string]map[*websocket.Conn]struct{}),
		disabled:    make(map[string]struct{}),
	}
}

func (h *AgentConnectionHub) Register(serverID string, connection *websocket.Conn) func() {
	h.mu.Lock()
	if _, disabled := h.disabled[serverID]; disabled {
		h.mu.Unlock()
		closeDisabledConnection(connection)
		return func() {}
	}
	if h.connections[serverID] == nil {
		h.connections[serverID] = make(map[*websocket.Conn]struct{})
	}
	h.connections[serverID][connection] = struct{}{}
	h.mu.Unlock()

	return func() {
		h.mu.Lock()
		delete(h.connections[serverID], connection)
		if len(h.connections[serverID]) == 0 {
			delete(h.connections, serverID)
		}
		h.mu.Unlock()
	}
}

func (h *AgentConnectionHub) Disconnect(serverID string) {
	h.mu.Lock()
	connections := h.connections[serverID]
	delete(h.connections, serverID)
	h.mu.Unlock()

	for connection := range connections {
		closeDisabledConnection(connection)
	}
}

func (h *AgentConnectionHub) SetEnabled(serverID string, enabled bool) {
	h.mu.Lock()
	if enabled {
		delete(h.disabled, serverID)
		h.mu.Unlock()
		return
	}
	h.disabled[serverID] = struct{}{}
	connections := h.connections[serverID]
	delete(h.connections, serverID)
	h.mu.Unlock()

	for connection := range connections {
		closeDisabledConnection(connection)
	}
}

func closeDisabledConnection(connection *websocket.Conn) {
	go func() {
		_ = connection.Close(websocket.StatusPolicyViolation, "服务器已停用")
	}()
}
