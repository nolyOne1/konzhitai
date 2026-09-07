package agentupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

const DefaultRoot = "/var/lib/yunling-agent/upgrades"

var (
	ErrInvalidArchive   = errors.New("代理升级包内容无效")
	ErrArtifactMismatch = errors.New("代理升级包与命令不一致")
	ErrVersionMismatch  = errors.New("代理版本不一致")
	ErrInvalidCommandID = errors.New("升级命令编号无效")
)

type Spec struct {
	CommandID        string                      `json:"command_id"`
	Action           agentprotocol.UpgradeAction `json:"action"`
	SourceVersion    string                      `json:"source_version"`
	TargetVersion    string                      `json:"target_version"`
	SHA256           string                      `json:"sha256"`
	ByteSize         int64                       `json:"byte_size"`
	ReconnectTimeout time.Duration               `json:"reconnect_timeout"`
	Phase            string                      `json:"phase"`
	StageDir         string                      `json:"stage_dir"`
	ArchivePath      string                      `json:"archive_path"`
	BackupDir        string                      `json:"backup_dir"`
	InstallRoot      string                      `json:"install_root"`
}

func loadSpec(root, commandID string) (Spec, error) {
	body, err := os.ReadFile(filepath.Join(root, commandID, "spec.json"))
	if err != nil {
		return Spec{}, err
	}
	var spec Spec
	if err := json.Unmarshal(body, &spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func saveSpec(root string, spec Spec) error {
	directory := filepath.Join(root, spec.CommandID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	temporary := filepath.Join(directory, "spec.json.tmp")
	if err := os.WriteFile(temporary, body, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(directory, "spec.json"))
}

func validCommandID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
