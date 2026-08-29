package audit

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrInvalidEvent = errors.New("审计事件内容无效")

type Repository interface {
	Append(context.Context, Event) error
	List(context.Context, Filter) ([]Event, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (s *Service) Record(ctx context.Context, event Event) error {
	if s == nil || s.repository == nil || strings.TrimSpace(event.Action) == "" ||
		strings.TrimSpace(event.TargetType) == "" || strings.TrimSpace(event.TargetID) == "" {
		return ErrInvalidEvent
	}
	event.ID = uuid.NewString()
	event.Action = strings.TrimSpace(event.Action)
	event.TargetType = strings.TrimSpace(event.TargetType)
	event.TargetID = strings.TrimSpace(event.TargetID)
	event.CreatedAt = s.now().UTC()
	if event.Details == nil {
		event.Details = map[string]any{}
	}
	return s.repository.Append(ctx, event)
}

func (s *Service) List(ctx context.Context, filter Filter) ([]Event, error) {
	if s == nil || s.repository == nil {
		return nil, ErrInvalidEvent
	}
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 200
	}
	events, err := s.repository.List(ctx, filter)
	if events == nil && err == nil {
		events = []Event{}
	}
	return events, err
}
