package agentupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type SystemController interface {
	DaemonReload(context.Context) error
	RestartAgent(context.Context) error
	WaitForConfirmation(context.Context, string) error
}

var managedTargets = map[string]string{
	"yunling-agent":                  "usr/local/bin/yunling-agent",
	"yunling-agent.service":          "etc/systemd/system/yunling-agent.service",
	"yunling-run@.service":           "etc/systemd/system/yunling-run@.service",
	"yunling-agent-upgrade@.service": "etc/systemd/system/yunling-agent-upgrade@.service",
	"50-yunling-agent.rules":         "etc/polkit-1/rules.d/50-yunling-agent.rules",
}

func Apply(root, commandID string, system SystemController) error {
	if !validCommandID(commandID) {
		return ErrInvalidCommandID
	}
	spec, err := loadSpec(root, commandID)
	if err != nil {
		return err
	}
	if spec.Action == "rollback" {
		return applyRollback(root, &spec, system)
	}
	if spec.Phase == "staged" {
		if err := verifyStagedArchive(spec); err != nil {
			return err
		}
		if err := backupManaged(spec); err != nil {
			return err
		}
		if err := replaceManaged(spec, spec.StageDir); err != nil {
			return err
		}
		spec.Phase = "files_replaced"
		if err := saveSpec(root, spec); err != nil {
			return err
		}
	}
	if spec.Phase == "files_replaced" {
		if err := system.DaemonReload(context.Background()); err != nil {
			return rollbackAfterFailure(root, &spec, system, err)
		}
		if err := system.RestartAgent(context.Background()); err != nil {
			return rollbackAfterFailure(root, &spec, system, err)
		}
		spec.Phase = "restarted"
		if err := saveSpec(root, spec); err != nil {
			return err
		}
	}
	if spec.Phase == "restarted" {
		ctx, cancel := context.WithTimeout(context.Background(), spec.ReconnectTimeout)
		defer cancel()
		if err := system.WaitForConfirmation(ctx, filepath.Join(root, commandID, "connected")); err != nil {
			return rollbackAfterFailure(root, &spec, system, err)
		}
		spec.Phase = "succeeded"
		if err := saveSpec(root, spec); err != nil {
			return err
		}
		_ = replaceDirectory(filepath.Join(root, "previous"), spec.BackupDir)
	}
	return nil
}

func applyRollback(root string, spec *Spec, system SystemController) error {
	if err := replaceManaged(*spec, spec.BackupDir); err != nil {
		return err
	}
	if err := system.DaemonReload(context.Background()); err != nil {
		return err
	}
	if err := system.RestartAgent(context.Background()); err != nil {
		return err
	}
	spec.Phase = "rolled_back"
	return saveSpec(root, *spec)
}
func rollbackAfterFailure(root string, spec *Spec, system SystemController, cause error) error {
	restore := replaceManaged(*spec, spec.BackupDir)
	_ = system.DaemonReload(context.Background())
	_ = system.RestartAgent(context.Background())
	spec.Phase = "rolled_back"
	_ = saveSpec(root, *spec)
	if restore != nil {
		return errors.Join(cause, restore)
	}
	return fmt.Errorf("代理升级失败并已回滚：%w", cause)
}

func backupManaged(spec Spec) error {
	if err := os.MkdirAll(spec.BackupDir, 0o700); err != nil {
		return err
	}
	for name, target := range managedTargets {
		source := filepath.Join(spec.InstallRoot, filepath.FromSlash(target))
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(filepath.Join(spec.BackupDir, name+".missing"), nil, 0o600); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return err
		}
		if err := copyAtomic(source, filepath.Join(spec.BackupDir, name), 0o600); err != nil {
			return err
		}
	}
	return nil
}
func replaceManaged(spec Spec, sourceRoot string) error {
	for name, target := range managedTargets {
		destination := filepath.Join(spec.InstallRoot, filepath.FromSlash(target))
		if _, err := os.Stat(filepath.Join(sourceRoot, name+".missing")); err == nil {
			if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		source := filepath.Join(sourceRoot, name)
		info, err := os.Stat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if name == "yunling-agent" {
			mode = 0o755
		}
		if err := copyAtomic(source, destination, mode); err != nil {
			return err
		}
		_ = info
	}
	return nil
}

func verifyStagedArchive(spec Spec) error {
	file, err := os.Open(spec.ArchivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, spec.ByteSize+1))
	if err != nil {
		return err
	}
	if size != spec.ByteSize || hex.EncodeToString(hash.Sum(nil)) != spec.SHA256 {
		return ErrArtifactMismatch
	}
	return nil
}
func copyAtomic(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary := destination + ".yunling.tmp"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chmod(temporary, mode); err != nil {
		return err
	}
	return os.Rename(temporary, destination)
}
func replaceDirectory(destination, source string) error {
	_ = os.RemoveAll(destination)
	return copyTree(source, destination)
}
func copyTree(source, destination string) error {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := copyAtomic(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name()), 0o600); err != nil {
			return err
		}
	}
	return nil
}
