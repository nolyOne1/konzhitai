package audit

import "time"

type Event struct {
	ID         string         `json:"id"`
	ActorID    string         `json:"actorId,omitempty"`
	Action     string         `json:"action"`
	TargetType string         `json:"targetType"`
	TargetID   string         `json:"targetId"`
	Details    map[string]any `json:"details"`
	IPAddress  string         `json:"ipAddress,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
}

type Filter struct {
	ActorID    string
	Action     string
	TargetType string
	Limit      int
}
