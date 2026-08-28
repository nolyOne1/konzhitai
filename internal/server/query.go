package server

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrServerNotFound      = errors.New("服务器不存在")
	ErrInvalidServerUpdate = errors.New("服务器更新内容无效")
)

type Dashboard struct {
	OnlineServers    int           `json:"onlineServers"`
	TotalServers     int           `json:"totalServers"`
	RunningRuns      int           `json:"runningRuns"`
	QueuedRuns       int           `json:"queuedRuns"`
	TodaySuccessRate float64       `json:"todaySuccessRate"`
	Servers          []ServerView  `json:"servers"`
	RecentEvents     []RecentEvent `json:"recentEvents"`
}

type RecentEvent struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurredAt"`
}

type ServerView struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	CloudProvider        string            `json:"cloudProvider"`
	Region               string            `json:"region"`
	Status               Status            `json:"status"`
	Enabled              bool              `json:"enabled"`
	Draining             bool              `json:"draining"`
	Labels               map[string]string `json:"labels"`
	Runtimes             []string          `json:"runtimes"`
	AgentVersion         string            `json:"agentVersion"`
	SchedulingWeight     int               `json:"schedulingWeight"`
	CPUUsagePercent      float64           `json:"cpuUsagePercent"`
	MemoryTotalBytes     int64             `json:"memoryTotalBytes"`
	MemoryAvailableBytes int64             `json:"memoryAvailableBytes"`
	DiskTotalBytes       int64             `json:"diskTotalBytes"`
	DiskAvailableBytes   int64             `json:"diskAvailableBytes"`
	RunningTasks         int               `json:"runningTasks"`
	LastSeenAt           *time.Time        `json:"lastSeenAt"`
}

type UpdateServerInput struct {
	Name             *string            `json:"name"`
	Labels           *map[string]string `json:"labels"`
	SchedulingWeight *int               `json:"schedulingWeight"`
	Enabled          *bool              `json:"enabled"`
	Draining         *bool              `json:"draining"`
}

type ManagementQuery interface {
	Dashboard(ctx context.Context) (Dashboard, error)
	ListServers(ctx context.Context) ([]ServerView, error)
	UpdateServer(ctx context.Context, id string, input UpdateServerInput) (ServerView, error)
}

type AgentDisconnector interface {
	Disconnect(serverID string)
}

type agentAvailabilityController interface {
	SetEnabled(serverID string, enabled bool)
}

type ManagementService struct {
	repository   ManagementQuery
	disconnector AgentDisconnector
}

func NewManagementService(repository ManagementQuery, disconnector AgentDisconnector) *ManagementService {
	if disconnector == nil {
		disconnector = noopAgentDisconnector{}
	}
	return &ManagementService{repository: repository, disconnector: disconnector}
}

func (s *ManagementService) Dashboard(ctx context.Context) (Dashboard, error) {
	return s.repository.Dashboard(ctx)
}

func (s *ManagementService) ListServers(ctx context.Context) ([]ServerView, error) {
	return s.repository.ListServers(ctx)
}

func (s *ManagementService) UpdateServer(
	ctx context.Context,
	id string,
	input UpdateServerInput,
) (ServerView, error) {
	if strings.TrimSpace(id) == "" || !validServerUpdate(input) {
		return ServerView{}, ErrInvalidServerUpdate
	}
	updated, err := s.repository.UpdateServer(ctx, id, input)
	if err != nil {
		return ServerView{}, err
	}
	if input.Enabled != nil {
		if controller, ok := s.disconnector.(agentAvailabilityController); ok {
			controller.SetEnabled(id, *input.Enabled)
		} else if !*input.Enabled {
			s.disconnector.Disconnect(id)
		}
	}
	return updated, nil
}

func validServerUpdate(input UpdateServerInput) bool {
	if input.Name == nil && input.Labels == nil && input.SchedulingWeight == nil && input.Enabled == nil && input.Draining == nil {
		return false
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) == "" {
		return false
	}
	if input.SchedulingWeight != nil && (*input.SchedulingWeight < 1 || *input.SchedulingWeight > 1000) {
		return false
	}
	for key := range valueOrEmpty(input.Labels) {
		if strings.TrimSpace(key) == "" {
			return false
		}
	}
	return true
}

func valueOrEmpty(labels *map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	return *labels
}

type noopAgentDisconnector struct{}

func (noopAgentDisconnector) Disconnect(string) {}
