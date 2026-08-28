package audit

import "time"

type Event struct {
	ID         string
	ActorID    string
	Action     string
	TargetType string
	TargetID   string
	Details    map[string]any
	IPAddress  string
	CreatedAt  time.Time
}
