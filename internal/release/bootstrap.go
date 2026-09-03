package release

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var (
	ErrBootstrapExists   = errors.New("生产基线已经导入")
	ErrBootstrapConflict = errors.New("生产基线与现有状态冲突")
)

type BootstrapHost interface {
	CaptureAndTagImages(context.Context) (ServiceImages, error)
	CopyAgentRelease(context.Context, string) error
	PublishAgentVolume(context.Context, string, func(string) error) error
}

type Bootstrapper struct {
	RootDir       string
	ComposeFile   string
	OverrideFile  string
	AgentLockPath string
	Store         *StateStore
	Host          BootstrapHost
	Locker        Locker
	Now           func() time.Time
}

func (bootstrapper *Bootstrapper) Run(ctx context.Context) error {
	if ctx == nil || bootstrapper == nil || bootstrapper.RootDir == "" || bootstrapper.ComposeFile == "" ||
		bootstrapper.OverrideFile == "" || bootstrapper.AgentLockPath == "" || bootstrapper.Store == nil ||
		bootstrapper.Host == nil || bootstrapper.Locker == nil || bootstrapper.Now == nil {
		return errors.New("生产基线导入依赖无效")
	}
	releaseLock, err := bootstrapper.Locker.TryLock(filepath.Join(bootstrapper.RootDir, "releases", "bootstrap.lock"))
	if err != nil {
		return err
	}
	defer func() { _ = releaseLock() }()

	current, err := bootstrapper.Store.LoadCurrent()
	if err == nil {
		if current.TargetID != "bootstrap" || current.Origin != OriginBootstrap {
			return ErrBootstrapConflict
		}
		wantOverride, renderErr := RenderComposeOverride(current)
		if renderErr != nil {
			return renderErr
		}
		existing, readErr := os.ReadFile(bootstrapper.OverrideFile)
		if readErr == nil {
			if string(existing) != string(wantOverride) {
				return ErrBootstrapConflict
			}
			return ErrBootstrapExists
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("读取基线 Compose 覆盖：%w", readErr)
		}
		return writeFileAtomic(bootstrapper.OverrideFile, wantOverride, 0o600)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取现有生产状态：%w", err)
	}
	if _, err := os.Lstat(bootstrapper.OverrideFile); err == nil {
		return ErrBootstrapConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查 Compose 覆盖：%w", err)
	}

	lock, err := LoadAgentLock(bootstrapper.AgentLockPath)
	if err != nil {
		return err
	}
	images, err := bootstrapper.Host.CaptureAndTagImages(ctx)
	if err != nil {
		return fmt.Errorf("捕获当前容器镜像：%w", err)
	}
	temporaryRoot, err := os.MkdirTemp(bootstrapper.RootDir, ".bootstrap-agent-")
	if err != nil {
		return fmt.Errorf("创建代理基线临时目录：%w", err)
	}
	defer os.RemoveAll(temporaryRoot)
	if err := os.Chmod(temporaryRoot, 0o700); err != nil {
		return fmt.Errorf("限制代理基线临时目录权限：%w", err)
	}
	agentDirectory := filepath.Join(temporaryRoot, "agent")
	if err := os.Mkdir(agentDirectory, 0o700); err != nil {
		return fmt.Errorf("创建代理复制目录：%w", err)
	}
	if err := bootstrapper.Host.CopyAgentRelease(ctx, agentDirectory); err != nil {
		return fmt.Errorf("复制当前代理发布：%w", err)
	}
	if err := VerifyAgentReleaseDir(lock, agentDirectory); err != nil {
		return err
	}
	if err := bootstrapper.Host.PublishAgentVolume(ctx, agentDirectory, func(path string) error {
		return VerifyAgentReleaseDir(lock, path)
	}); err != nil {
		return fmt.Errorf("发布代理基线卷：%w", err)
	}

	migrationDigest, err := MigrationTreeDigest(filepath.Join(bootstrapper.RootDir, "migrations"))
	if err != nil {
		return err
	}
	composeDigest, err := FileSHA256(bootstrapper.ComposeFile)
	if err != nil {
		return err
	}
	contractDigest, err := digestEntries([]digestEntry{{path: "deploy/docker-compose.yml", digest: composeDigest}})
	if err != nil {
		return err
	}
	stored, err := bootstrapper.Store.CreateBootstrap(images, Compatibility{
		MigrationTreeSHA256: migrationDigest, DeploymentContractSHA256: contractDigest,
		AgentVersion: lock.Version, AgentManifestSHA256: lock.ManifestSHA256,
	}, bootstrapper.Now().UTC())
	if err != nil {
		return err
	}
	override, err := RenderComposeOverride(stored)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(bootstrapper.OverrideFile, override, 0o600); err != nil {
		return fmt.Errorf("写入基线 Compose 覆盖：%w", err)
	}
	return nil
}
