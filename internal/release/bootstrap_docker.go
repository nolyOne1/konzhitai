package release

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var dockerImageIDPattern = regexp.MustCompile(`^sha256:([0-9a-f]{64})$`)

type DockerBootstrapHost struct {
	runner       CommandRunner
	apiContainer string
	agentVolume  string
	apiImageID   string
}

func NewDockerBootstrapHost(runner CommandRunner, apiContainer, agentVolume string) *DockerBootstrapHost {
	return &DockerBootstrapHost{runner: runner, apiContainer: apiContainer, agentVolume: agentVolume}
}

func (host *DockerBootstrapHost) CaptureAndTagImages(ctx context.Context) (ServiceImages, error) {
	if ctx == nil || host == nil || host.runner == nil {
		return ServiceImages{}, errors.New("Docker 基线依赖无效")
	}
	containers := []struct {
		service   string
		container string
	}{
		{service: "api", container: "yunling-api-1"},
		{service: "scheduler", container: "yunling-scheduler-1"},
		{service: "web", container: "yunling-web-1"},
		{service: "ops", container: "yunling-ops-1"},
	}
	images := ServiceImages{}
	for _, item := range containers {
		result, err := runSuccessful(ctx, host.runner, "docker", []string{
			"inspect", "--type", "container", "--format", "{{.Image}}", item.container,
		})
		if err != nil {
			return ServiceImages{}, fmt.Errorf("检查容器 %s：%w", item.container, err)
		}
		match := dockerImageIDPattern.FindStringSubmatch(strings.TrimSpace(string(result.Stdout)))
		if len(match) != 2 {
			return ServiceImages{}, fmt.Errorf("容器 %s 的镜像 ID 无效", item.container)
		}
		tag := "yunling-local-bootstrap/" + item.service + ":" + match[1][:12]
		if _, err := runSuccessful(ctx, host.runner, "docker", []string{"image", "tag", "sha256:" + match[1], tag}); err != nil {
			return ServiceImages{}, fmt.Errorf("标记基线镜像 %s：%w", item.service, err)
		}
		switch item.service {
		case "api":
			images.API = tag
			host.apiImageID = "sha256:" + match[1]
		case "scheduler":
			images.Scheduler = tag
		case "web":
			images.Web = tag
		case "ops":
			images.Ops = tag
		}
	}
	return images, nil
}

func (host *DockerBootstrapHost) CopyAgentRelease(ctx context.Context, destination string) error {
	if ctx == nil || host == nil || host.runner == nil || host.apiContainer == "" || destination == "" {
		return errors.New("复制代理发布参数无效")
	}
	info, err := os.Lstat(destination)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("代理复制目标必须是现有普通目录")
	}
	_, err = runSuccessful(ctx, host.runner, "docker", []string{
		"cp", host.apiContainer + ":/opt/yunling/releases/agent/.", filepath.Clean(destination),
	})
	return err
}

func (host *DockerBootstrapHost) PublishAgentVolume(ctx context.Context, source string, verify func(string) error) error {
	if ctx == nil || host == nil || host.runner == nil || host.agentVolume != "yunling_agent_releases" ||
		host.apiImageID == "" || source == "" || verify == nil {
		return errors.New("Docker 代理卷发布参数无效")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("代理卷来源必须是现有普通目录")
	}
	token, err := randomDockerSuffix()
	if err != nil {
		return fmt.Errorf("创建 Docker 卷事务编号：%w", err)
	}
	stagingVolume := host.agentVolume + "_stage_" + token
	if _, err := runSuccessful(ctx, host.runner, "docker", []string{
		"volume", "create", "--label", "yunling.bootstrap=staging", stagingVolume,
	}); err != nil {
		return fmt.Errorf("创建代理临时卷：%w", err)
	}
	defer func() {
		_, _ = host.runner.Run(context.Background(), "docker", []string{"volume", "rm", "--force", stagingVolume}, nil)
	}()
	if err := host.populateVolume(ctx, stagingVolume, source, false); err != nil {
		return err
	}
	if err := host.verifyVolume(ctx, stagingVolume, verify); err != nil {
		return fmt.Errorf("临时代理卷校验失败：%w", err)
	}

	exists, err := host.volumeExists(ctx, host.agentVolume)
	if err != nil {
		return err
	}
	if exists {
		if err := host.verifyVolume(ctx, host.agentVolume, verify); err != nil {
			return fmt.Errorf("%w：现有代理卷非空且内容不匹配：%v", ErrBootstrapConflict, err)
		}
		return nil
	}

	ownershipLabel := "yunling.bootstrap.install=" + token
	if _, err := runSuccessful(ctx, host.runner, "docker", []string{
		"volume", "create", "--label", ownershipLabel, host.agentVolume,
	}); err != nil {
		return fmt.Errorf("创建正式代理卷：%w", err)
	}
	labelResult, err := runSuccessful(ctx, host.runner, "docker", []string{
		"volume", "inspect", "--format", "{{index .Labels \"yunling.bootstrap.install\"}}", host.agentVolume,
	})
	if err != nil || strings.TrimSpace(string(labelResult.Stdout)) != token {
		return fmt.Errorf("%w：正式代理卷在创建期间被其他操作占用", ErrBootstrapConflict)
	}
	createdFinal := true
	defer func() {
		if createdFinal {
			_, _ = host.runner.Run(context.Background(), "docker", []string{"volume", "rm", "--force", host.agentVolume}, nil)
		}
	}()
	if err := host.populateVolume(ctx, host.agentVolume, source, false); err != nil {
		return err
	}
	if err := host.verifyVolume(ctx, host.agentVolume, verify); err != nil {
		return fmt.Errorf("正式代理卷校验失败：%w", err)
	}
	createdFinal = false
	return nil
}

func (host *DockerBootstrapHost) volumeExists(ctx context.Context, volume string) (bool, error) {
	result, err := runSuccessful(ctx, host.runner, "docker", []string{
		"volume", "ls", "--quiet", "--filter", "name=^" + volume + "$",
	})
	if err != nil {
		return false, fmt.Errorf("检查正式代理卷：%w", err)
	}
	output := strings.TrimSpace(string(result.Stdout))
	if output == "" {
		return false, nil
	}
	if output != volume {
		return false, fmt.Errorf("%w：Docker 返回了意外卷名称", ErrBootstrapConflict)
	}
	return true, nil
}

func (host *DockerBootstrapHost) populateVolume(ctx context.Context, volume, source string, readOnly bool) error {
	helper, err := host.createVolumeHelper(ctx, volume, readOnly)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = host.runner.Run(context.Background(), "docker", []string{"rm", "--force", helper}, nil)
	}()
	if _, err := runSuccessful(ctx, host.runner, "docker", []string{
		"cp", filepath.Join(source, "."), helper + ":/release",
	}); err != nil {
		return fmt.Errorf("写入代理卷：%w", err)
	}
	return nil
}

func (host *DockerBootstrapHost) verifyVolume(ctx context.Context, volume string, verify func(string) error) error {
	helper, err := host.createVolumeHelper(ctx, volume, true)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = host.runner.Run(context.Background(), "docker", []string{"rm", "--force", helper}, nil)
	}()
	temporary, err := os.MkdirTemp("", ".yunling-volume-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return err
	}
	if _, err := runSuccessful(ctx, host.runner, "docker", []string{
		"cp", helper + ":/release/.", temporary,
	}); err != nil {
		return fmt.Errorf("读取代理卷：%w", err)
	}
	return verify(temporary)
}

func (host *DockerBootstrapHost) createVolumeHelper(ctx context.Context, volume string, readOnly bool) (string, error) {
	token, err := randomDockerSuffix()
	if err != nil {
		return "", err
	}
	helper := "yunling-agent-volume-" + token
	mount := "type=volume,src=" + volume + ",dst=/release"
	if readOnly {
		mount += ",readonly"
	}
	if _, err := runSuccessful(ctx, host.runner, "docker", []string{
		"create", "--name", helper, "--mount", mount, "--entrypoint", "/bin/sh", host.apiImageID, "-c", "true",
	}); err != nil {
		return "", fmt.Errorf("创建代理卷校验容器：%w", err)
	}
	return helper, nil
}

func randomDockerSuffix() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
