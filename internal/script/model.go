package script

import "time"

type SyncState string

const (
	SyncPending SyncState = "pending"
	Syncing     SyncState = "syncing"
	Synced      SyncState = "synced"
	SyncFailed  SyncState = "failed"
	SyncDrifted SyncState = "drifted"
)

type Script struct {
	ID          string
	Name        string
	Description string
	Runtime     string
	CreatedAt   time.Time
}

type Version struct {
	ID             string
	ScriptID       string
	Number         int
	ArtifactURI    string
	ArtifactSHA256 string
	Entrypoint     string
	CreatedAt      time.Time
}
