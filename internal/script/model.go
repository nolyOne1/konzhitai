package script

import (
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

type SyncState = agentprotocol.SyncState

const (
	SyncPending     = agentprotocol.SyncPending
	SyncDownloading = agentprotocol.SyncDownloading
	SyncReady       = agentprotocol.SyncReady
	SyncFailed      = agentprotocol.SyncFailed
	SyncDrifted     = agentprotocol.SyncDrifted
)

type Script struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	Runtime          string     `json:"runtime"`
	Category         string     `json:"category"`
	Tags             []string   `json:"tags"`
	CurrentVersionID string     `json:"currentVersionId"`
	CurrentVersion   int        `json:"currentVersion"`
	DraftUpdatedAt   *time.Time `json:"draftUpdatedAt"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

type Version struct {
	ID             string    `json:"id"`
	ScriptID       string    `json:"scriptId"`
	Number         int       `json:"number"`
	ArtifactURI    string    `json:"artifactUri"`
	ArtifactSHA256 string    `json:"artifactSha256"`
	Entrypoint     string    `json:"entrypoint"`
	Manifest       Manifest  `json:"manifest"`
	ReleaseNotes   string    `json:"releaseNotes"`
	CreatedBy      string    `json:"createdBy"`
	CreatedAt      time.Time `json:"createdAt"`
}

type DistributionMode string

const (
	DistributionAllCompatible DistributionMode = "all_compatible"
	DistributionServerGroup   DistributionMode = "server_group"
	DistributionLabels        DistributionMode = "labels"
	DistributionOnDemand      DistributionMode = "on_demand"
)

type DistributionRule struct {
	Mode          DistributionMode  `json:"mode"`
	ServerGroupID string            `json:"serverGroupId,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type ParameterDefinition struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
}

type ResourceRequirements struct {
	CPUMillicores int   `json:"cpuMillicores"`
	MemoryBytes   int64 `json:"memoryBytes"`
	DiskBytes     int64 `json:"diskBytes"`
}

type Manifest struct {
	Runtime              string                `json:"runtime"`
	Entrypoint           string                `json:"entrypoint"`
	Category             string                `json:"category"`
	Tags                 []string              `json:"tags"`
	Distribution         DistributionRule      `json:"distribution"`
	ParameterDefinitions []ParameterDefinition `json:"parameterDefinitions,omitempty"`
	Resources            ResourceRequirements  `json:"resources"`
}

type Draft struct {
	ScriptID      string    `json:"scriptId"`
	BaseVersionID string    `json:"baseVersionId"`
	Content       string    `json:"content"`
	Manifest      Manifest  `json:"manifest"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type SyncView struct {
	ID             string                  `json:"id"`
	ServerID       string                  `json:"serverId"`
	ServerName     string                  `json:"serverName"`
	ScriptID       string                  `json:"scriptId"`
	VersionID      string                  `json:"versionId"`
	VersionNumber  int                     `json:"versionNumber"`
	State          agentprotocol.SyncState `json:"state"`
	ArtifactSHA256 string                  `json:"artifactSha256"`
	ErrorCode      string                  `json:"errorCode"`
	ErrorMessage   string                  `json:"errorMessage"`
	Blocked        bool                    `json:"blocked"`
	SyncedAt       *time.Time              `json:"syncedAt"`
	UpdatedAt      time.Time               `json:"updatedAt"`
}
