package agentprotocol

import "time"

type LogChunk struct {
	MessageType    string    `json:"message_type"`
	RunID          string    `json:"run_id"`
	ExecutionToken string    `json:"execution_token"`
	Sequence       uint64    `json:"sequence"`
	Stream         string    `json:"stream"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"created_at"`
}

type LogAcknowledgement struct {
	MessageType    string `json:"message_type"`
	RunID          string `json:"run_id"`
	ExecutionToken string `json:"execution_token"`
	Stream         string `json:"stream"`
	NextSequence   uint64 `json:"next_sequence"`
}
