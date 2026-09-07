package agentupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"yunling.local/platform/internal/agentprotocol"
)

type Downloader interface {
	Download(context.Context, string) (io.ReadCloser, error)
}
type ApplyStarter interface {
	StartUpgrade(context.Context, string) error
}
type BinaryVersionReader func(context.Context, string) (string, error)

type Manager struct {
	root          string
	downloader    Downloader
	starter       ApplyStarter
	binaryVersion BinaryVersionReader
	installRoot   string
}

func NewManager(root string, downloader Downloader, starter ApplyStarter, binaryVersion BinaryVersionReader) *Manager {
	if binaryVersion == nil {
		binaryVersion = readBinaryVersion
	}
	return &Manager{root: root, downloader: downloader, starter: starter, binaryVersion: binaryVersion, installRoot: "/"}
}

func (m *Manager) WithInstallRoot(root string) *Manager { m.installRoot = root; return m }

func (m *Manager) Stage(ctx context.Context, command agentprotocol.UpgradeCommand) (Spec, error) {
	if !validCommandID(command.CommandID) {
		return Spec{}, ErrInvalidCommandID
	}
	if existing, err := loadSpec(m.root, command.CommandID); err == nil {
		if existing.TargetVersion != command.TargetVersion || existing.Action != command.Action {
			return Spec{}, ErrArtifactMismatch
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Spec{}, err
	}
	if command.Action != agentprotocol.UpgradeInstall || command.DownloadURL == "" || command.ByteSize <= 0 || len(command.SHA256) != 64 {
		return Spec{}, ErrArtifactMismatch
	}
	body, err := m.downloader.Download(ctx, command.DownloadURL)
	if err != nil {
		return Spec{}, err
	}
	defer body.Close()
	archive, err := io.ReadAll(io.LimitReader(body, command.ByteSize+1))
	if err != nil {
		return Spec{}, err
	}
	digest := sha256.Sum256(archive)
	if int64(len(archive)) != command.ByteSize || hex.EncodeToString(digest[:]) != command.SHA256 {
		return Spec{}, ErrArtifactMismatch
	}
	commandDir := filepath.Join(m.root, command.CommandID)
	if err := os.MkdirAll(commandDir, 0o700); err != nil {
		return Spec{}, err
	}
	downloadTemporary := filepath.Join(commandDir, "download.tmp")
	archivePath := filepath.Join(commandDir, "artifact.tar.gz")
	if err := os.WriteFile(downloadTemporary, archive, 0o600); err != nil {
		return Spec{}, err
	}
	stageDir := filepath.Join(commandDir, "stage")
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return Spec{}, err
	}
	if err := extractArchive(archive, stageDir, command.TargetVersion); err != nil {
		return Spec{}, err
	}
	if err := os.Rename(downloadTemporary, archivePath); err != nil {
		return Spec{}, err
	}
	version, err := m.binaryVersion(ctx, filepath.Join(stageDir, "yunling-agent"))
	if err != nil || strings.TrimSpace(version) != command.TargetVersion {
		return Spec{}, ErrVersionMismatch
	}
	spec := Spec{CommandID: command.CommandID, TargetID: command.TargetID, Action: command.Action, SourceVersion: command.SourceVersion, TargetVersion: command.TargetVersion, SHA256: command.SHA256, ByteSize: command.ByteSize, ReconnectTimeout: command.ReconnectTimeout, Phase: "staged", StageDir: stageDir, ArchivePath: archivePath, BackupDir: filepath.Join(commandDir, "backup"), InstallRoot: m.installRoot}
	if spec.ReconnectTimeout <= 0 {
		spec.ReconnectTimeout = 2 * 60 * 1e9
	}
	if err := saveSpec(m.root, spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func (m *Manager) StartApply(ctx context.Context, commandID string) error {
	if !validCommandID(commandID) {
		return ErrInvalidCommandID
	}
	err := m.starter.StartUpgrade(ctx, commandID)
	if err == nil {
		return nil
	}
	if spec, loadErr := loadSpec(m.root, commandID); loadErr == nil && (spec.Phase == "rollback_failed" || spec.Phase == "rollback_ready") {
		return fmt.Errorf("%w：%v", ErrRollbackFailed, err)
	}
	return err
}

func (m *Manager) Rollback(ctx context.Context, command agentprotocol.UpgradeCommand) error {
	if !validCommandID(command.CommandID) {
		return ErrInvalidCommandID
	}
	if command.Action != agentprotocol.UpgradeRollback || strings.TrimSpace(command.TargetVersion) == "" {
		return ErrArtifactMismatch
	}
	if existing, err := loadSpec(m.root, command.CommandID); err == nil {
		if existing.Action != agentprotocol.UpgradeRollback || existing.TargetVersion != command.TargetVersion {
			return ErrArtifactMismatch
		}
		return m.StartApply(ctx, command.CommandID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	backup := filepath.Join(m.root, "previous")
	if command.InstallCommandID != "" {
		if !validCommandID(command.InstallCommandID) {
			return ErrInvalidCommandID
		}
		installSpec, err := loadSpec(m.root, command.InstallCommandID)
		if errors.Is(err, os.ErrNotExist) {
			return ErrNoRollbackNeeded
		}
		if err != nil {
			return err
		}
		if installSpec.Phase == "staged" || installSpec.Phase == "backup_complete" || installSpec.Phase == "rolled_back" {
			return ErrNoRollbackNeeded
		}
		backup = installSpec.BackupDir
	}
	if info, err := os.Stat(backup); err != nil || !info.IsDir() {
		if err != nil {
			return err
		}
		return ErrArtifactMismatch
	}
	spec := Spec{
		CommandID: command.CommandID, TargetID: command.TargetID, Action: command.Action,
		SourceVersion: command.SourceVersion, TargetVersion: command.TargetVersion,
		Phase: "staged", BackupDir: backup, InstallRoot: m.installRoot,
	}
	if err := saveSpec(m.root, spec); err != nil {
		return err
	}
	return m.StartApply(ctx, command.CommandID)
}

func (m *Manager) RuntimeState() (*agentprotocol.UpgradeRuntimeState, error) {
	return LoadRuntimeState(m.root)
}

func (m *Manager) SaveRuntimeState(state agentprotocol.UpgradeRuntimeState) error {
	return SaveRuntimeState(m.root, state)
}

func extractArchive(body []byte, destination, version string) error {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return ErrInvalidArchive
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	allowed := map[string]bool{"agent-version": false, "yunling-agent": false, "install.sh": false, "yunling-agent.service": false, "yunling-run@.service": false, "yunling-agent-upgrade@.service": false, "50-yunling-agent.rules": false}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrInvalidArchive
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return ErrInvalidArchive
		}
		if _, ok := allowed[header.Name]; !ok || allowed[header.Name] || filepath.Base(header.Name) != header.Name {
			return ErrInvalidArchive
		}
		contents, err := io.ReadAll(io.LimitReader(tr, header.Size+1))
		if err != nil || int64(len(contents)) != header.Size {
			return ErrInvalidArchive
		}
		if header.Name == "agent-version" && strings.TrimSpace(string(contents)) != version {
			return ErrVersionMismatch
		}
		mode := os.FileMode(0o600)
		if header.Name == "yunling-agent" || header.Name == "install.sh" {
			mode = 0o700
		}
		if err := os.WriteFile(filepath.Join(destination, header.Name), contents, mode); err != nil {
			return err
		}
		allowed[header.Name] = true
	}
	for _, found := range allowed {
		if !found {
			return ErrInvalidArchive
		}
	}
	return nil
}

func readBinaryVersion(ctx context.Context, binary string) (string, error) {
	output, err := exec.CommandContext(ctx, binary, "version").Output()
	return strings.TrimSpace(string(output)), err
}

type HTTPDownloader struct{ Client *http.Client }

func (d HTTPDownloader) Download(ctx context.Context, url string) (io.ReadCloser, error) {
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("下载安装包失败：HTTP %d", response.StatusCode)
	}
	return response.Body, nil
}
