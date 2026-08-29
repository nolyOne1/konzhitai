package alert

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Severity string
type Status string

const (
	SeverityInfo       Severity = "info"
	SeverityWarning    Severity = "warning"
	SeverityCritical   Severity = "critical"
	StatusOpen         Status   = "open"
	StatusAcknowledged Status   = "acknowledged"
	StatusResolved     Status   = "resolved"
)

var (
	ErrInvalidEvent  = errors.New("告警事件内容无效")
	ErrAlertNotFound = errors.New("告警不存在")
)

type Event struct {
	ResourceType string   `json:"resourceType"`
	ResourceID   string   `json:"resourceId"`
	Code         string   `json:"code"`
	Severity     Severity `json:"severity"`
	Title        string   `json:"title"`
	Message      string   `json:"message"`
}

type Alert struct {
	ID string `json:"id"`
	Event
	Status          Status     `json:"status"`
	Occurrences     int        `json:"occurrences"`
	FirstOccurredAt time.Time  `json:"firstOccurredAt"`
	LastOccurredAt  time.Time  `json:"lastOccurredAt"`
	AcknowledgedBy  string     `json:"acknowledgedBy,omitempty"`
	AcknowledgedAt  *time.Time `json:"acknowledgedAt,omitempty"`
}

type Repository interface {
	MergeOrCreate(context.Context, Event, time.Time, time.Time) error
	List(context.Context) ([]Alert, error)
	Acknowledge(context.Context, string, string, time.Time) error
}

type Service struct {
	repository Repository
	now        func() time.Time
	window     time.Duration
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now, window: 5 * time.Minute}
}

func (s *Service) Raise(ctx context.Context, event Event) error {
	if s == nil || s.repository == nil || !validEvent(event) {
		return ErrInvalidEvent
	}
	now := s.now().UTC()
	return s.repository.MergeOrCreate(ctx, event, now, now.Add(-s.window))
}

func (s *Service) List(ctx context.Context) ([]Alert, error) {
	if s == nil || s.repository == nil {
		return nil, ErrInvalidEvent
	}
	items, err := s.repository.List(ctx)
	if items == nil && err == nil {
		items = []Alert{}
	}
	return items, err
}

func (s *Service) Acknowledge(ctx context.Context, id, userID string) error {
	if s == nil || s.repository == nil || strings.TrimSpace(id) == "" || strings.TrimSpace(userID) == "" {
		return ErrInvalidEvent
	}
	return s.repository.Acknowledge(ctx, id, userID, s.now().UTC())
}

func validEvent(event Event) bool {
	validSeverity := event.Severity == SeverityInfo || event.Severity == SeverityWarning || event.Severity == SeverityCritical
	return strings.TrimSpace(event.ResourceType) != "" && strings.TrimSpace(event.ResourceID) != "" &&
		strings.TrimSpace(event.Code) != "" && strings.TrimSpace(event.Title) != "" && validSeverity
}
