package agentprotocol

type SyncState string

const (
	SyncPending     SyncState = "pending"
	SyncDownloading SyncState = "downloading"
	SyncReady       SyncState = "ready"
	SyncFailed      SyncState = "failed"
	SyncDrifted     SyncState = "drifted"
)

type SyncCommand struct {
	ScriptID    string `json:"script_id"`
	VersionID   string `json:"version_id"`
	ArtifactURL string `json:"artifact_url"`
	SHA256      string `json:"sha256"`
}

type SyncResult struct {
	ScriptID     string    `json:"script_id"`
	VersionID    string    `json:"version_id"`
	State        SyncState `json:"state"`
	SHA256       string    `json:"sha256,omitempty"`
	ErrorCode    string    `json:"error_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
}
