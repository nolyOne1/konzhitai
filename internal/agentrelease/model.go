package agentrelease

import (
	"context"
	"errors"
	"io"
	"time"
)

const (
	ReleaseStatusAvailable = "available"
	ReleaseStatusWithdrawn = "withdrawn"
	SelfUpgradeCapability  = "self_upgrade_v1"
)

var (
	ErrReleaseInvalid     = errors.New("代理版本内容无效")
	ErrReleaseExists      = errors.New("代理版本已存在")
	ErrReleaseNotFound    = errors.New("代理版本不存在")
	ErrReleaseWithdrawn   = errors.New("代理版本已撤回")
	ErrRecommendedRelease = errors.New("当前推荐版本不能撤回")
	ErrArtifactMismatch   = errors.New("代理安装包内容与清单不一致")
)

type Artifact struct {
	ID          string `json:"id,omitempty"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	FileName    string `json:"file_name"`
	ByteSize    int64  `json:"byte_size"`
	SHA256      string `json:"sha256"`
	ObjectKey   string `json:"-"`
	DownloadURL string `json:"download_url"`
}

type Release struct {
	ID             string     `json:"id"`
	Version        string     `json:"version"`
	Status         string     `json:"status"`
	Recommended    bool       `json:"recommended"`
	ReleaseNotes   string     `json:"release_notes"`
	ManifestSHA256 string     `json:"manifest_sha256"`
	Capabilities   []string   `json:"capabilities"`
	Artifacts      []Artifact `json:"artifacts"`
	CreatedBy      string     `json:"created_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type ImportArtifact struct {
	OS       string
	Arch     string
	FileName string
	ByteSize int64
	SHA256   string
	Body     io.Reader
}

type ImportInput struct {
	Version        string
	ReleaseNotes   string
	ManifestSHA256 string
	CreatedBy      string
	Recommend      bool
	Artifacts      []ImportArtifact
}

type Repository interface {
	Create(context.Context, Release) (Release, error)
	List(context.Context) ([]Release, error)
	Recommended(context.Context) (Release, error)
	SetRecommended(context.Context, string) (Release, error)
	Withdraw(context.Context, string) (Release, error)
	FindArtifact(context.Context, string, string, string) (Release, Artifact, error)
}
