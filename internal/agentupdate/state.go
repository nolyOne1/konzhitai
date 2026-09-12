package agentupdate

import (
	"encoding/json"
	"errors"
	"fmt"
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
	ErrRollbackFailed   = errors.New("代理升级失败后的本地恢复未完成")
)

type Spec struct {
	CommandID        string                      `json:"command_id"`
	TargetID         string                      `json:"target_id"`
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
	return writeStateFile(filepath.Join(directory, "spec.json"), body)
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
	return writeStateFile(filepath.Join(root, "runtime.json"), body)
}

// Both the unprivileged agent and the root upgrade helper replace these files.
// The owning directory, not the current writer or a legacy root-owned file,
// determines who must be able to read the new private state after a restart.
func writeStateFile(path string, body []byte) error {
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("升级状态父路径不是普通目录：%s", directory)
	}
	file, err := os.CreateTemp(directory, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	defer file.Close()
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if err := inheritStateOwner(file, info); err != nil {
		return fmt.Errorf("保留升级状态目录属主失败：%w", err)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
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
