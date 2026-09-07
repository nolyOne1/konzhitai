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
	ErrNoRollbackNeeded = errors.New("本次升级尚未替换文件，无需回滚")
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

func SaveRuntimeState(root string, state agentprotocol.UpgradeRuntimeState) error {
	if !validCommandID(state.CommandID) {
		return ErrInvalidCommandID
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary := filepath.Join(root, "runtime.json.tmp")
	if err := os.WriteFile(temporary, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(root, "runtime.json")); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func LoadRuntimeState(root string) (*agentprotocol.UpgradeRuntimeState, error) {
	body, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state agentprotocol.UpgradeRuntimeState
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, err
	}
	if !validCommandID(state.CommandID) {
		return nil, ErrInvalidCommandID
	}
	return &state, nil
}

func ConfirmReconnect(root, commandID, version string) error {
	if !validCommandID(commandID) {
		return ErrInvalidCommandID
	}
	spec, err := loadSpec(root, commandID)
	if err != nil {
		return err
	}
	if spec.TargetVersion != version {
		return ErrVersionMismatch
	}
	state, err := LoadRuntimeState(root)
	if err != nil {
		return err
	}
	if state != nil && state.CommandID == commandID {
		state.Stage = agentprotocol.StageReconnecting
		state.UpdatedAt = time.Now().UTC()
		if err := SaveRuntimeState(root, *state); err != nil {
			return err
		}
	}
	directory := filepath.Join(root, commandID)
	temporary := filepath.Join(directory, "connected.tmp")
	if err := os.WriteFile(temporary, []byte(version+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(directory, "connected")); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
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
