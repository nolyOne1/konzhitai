package release

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultMinFreeBytes = uint64(3 << 30)
	defaultMinMemory    = uint64(512 << 20)
)

var (
	ErrInsufficientDisk        = errors.New("生产主机可用磁盘不足")
	ErrInsufficientMemory      = errors.New("生产主机可用内存不足")
	ErrInfrastructureUnhealthy = errors.New("生产基础设施不健康")
	ErrLockHeld                = errors.New("生产发布锁已被占用")
	ErrUnsupportedPlatform     = errors.New("当前平台不支持生产发布")
)

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type CommandRunner interface {
	Run(context.Context, string, []string, []byte) (CommandResult, error)
}

type ResourceReader interface {
	FreeBytes(string) (uint64, error)
	AvailableMemory() (uint64, error)
}

type Locker interface {
	TryLock(string) (func() error, error)
}

type HostConfig struct {
	RootDir       string
	ComposeFile   string
	OverrideFile  string
	EnvFile       string
	ProjectName   string
	PublicBaseURL string
	MinFreeBytes  uint64
	MinMemory     uint64
}

type OSCommandRunner struct{}

type HostResources struct{}

type HostLocker struct{}

func NewCommandRunner() CommandRunner {
	return OSCommandRunner{}
}

func NewResourceReader() ResourceReader {
	return HostResources{}
}

func NewLocker() Locker {
	return HostLocker{}
}

func (OSCommandRunner) Run(ctx context.Context, name string, args []string, stdin []byte) (CommandResult, error) {
	if ctx == nil || name == "" {
		return CommandResult{}, errors.New("命令参数无效")
	}
	command := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
	}
	return result, err
}

func (HostResources) FreeBytes(path string) (uint64, error) {
	return platformFreeBytes(path)
}

func (HostResources) AvailableMemory() (uint64, error) {
	return platformAvailableMemory()
}

func (HostLocker) TryLock(path string) (func() error, error) {
	return platformTryLock(path)
}

func Preflight(ctx context.Context, config HostConfig, runner CommandRunner, resources ResourceReader) error {
	if ctx == nil || runner == nil || resources == nil {
		return errors.New("生产预检依赖无效")
	}
	config = normalizeHostConfig(config)
	freeBytes, err := resources.FreeBytes(config.RootDir)
	if err != nil {
		return fmt.Errorf("读取生产磁盘余量：%w", err)
	}
	if freeBytes < config.MinFreeBytes {
		return fmt.Errorf("%w：当前 %d 字节，至少需要 %d 字节", ErrInsufficientDisk, freeBytes, config.MinFreeBytes)
	}
	availableMemory, err := resources.AvailableMemory()
	if err != nil {
		return fmt.Errorf("读取生产可用内存：%w", err)
	}
	if availableMemory < config.MinMemory {
		return fmt.Errorf("%w：当前 %d 字节，至少需要 %d 字节", ErrInsufficientMemory, availableMemory, config.MinMemory)
	}

	if _, err := runSuccessful(ctx, runner, "docker", []string{"version"}); err != nil {
		return fmt.Errorf("Docker 不可用：%w", err)
	}
	if _, err := runSuccessful(ctx, runner, "docker", []string{"compose", "version"}); err != nil {
		return fmt.Errorf("Docker Compose 不可用：%w", err)
	}
	result, err := runSuccessful(ctx, runner, "docker", append(composePrefix(config),
		"ps", "--format", "json", "postgres", "redis", "minio", "caddy"))
	if err != nil {
		return fmt.Errorf("读取基础设施状态：%w", err)
	}
	if err := validateInfrastructure(result.Stdout); err != nil {
		return err
	}
	return nil
}

func normalizeHostConfig(config HostConfig) HostConfig {
	if config.RootDir == "" {
		config.RootDir = "/opt/yunling"
	}
	if config.ComposeFile == "" {
		config.ComposeFile = filepath.Join(config.RootDir, "deploy", "docker-compose.yml")
	}
	if config.OverrideFile == "" {
		config.OverrideFile = filepath.Join(config.RootDir, "deploy", "docker-compose.release.yml")
	}
	if config.EnvFile == "" {
		config.EnvFile = filepath.Join(config.RootDir, "deploy", ".env")
	}
	if config.ProjectName == "" {
		config.ProjectName = "yunling"
	}
	if config.MinFreeBytes == 0 {
		config.MinFreeBytes = defaultMinFreeBytes
	}
	if config.MinMemory == 0 {
		config.MinMemory = defaultMinMemory
	}
	return config
}

func composePrefix(config HostConfig) []string {
	return []string{
		"compose", "--project-name", config.ProjectName, "--env-file", config.EnvFile,
		"-f", config.ComposeFile, "-f", config.OverrideFile,
	}
}

func runSuccessful(ctx context.Context, runner CommandRunner, name string, args []string) (CommandResult, error) {
	result, err := runner.Run(ctx, name, args, nil)
	if err != nil {
		return result, err
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("命令退出码为 %d", result.ExitCode)
	}
	return result, nil
}

type composeProcess struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

func validateInfrastructure(data []byte) error {
	processes, err := decodeComposeProcesses(data)
	if err != nil {
		return fmt.Errorf("%w：无法解析容器状态：%v", ErrInfrastructureUnhealthy, err)
	}
	states := make(map[string]composeProcess, len(processes))
	for _, process := range processes {
		if process.Service == "" {
			return fmt.Errorf("%w：容器服务名为空", ErrInfrastructureUnhealthy)
		}
		if _, exists := states[process.Service]; exists {
			return fmt.Errorf("%w：服务 %s 状态重复", ErrInfrastructureUnhealthy, process.Service)
		}
		states[process.Service] = process
	}
	for _, service := range []string{"postgres", "redis", "minio", "caddy"} {
		process, exists := states[service]
		if !exists {
			return fmt.Errorf("%w：缺少服务 %s", ErrInfrastructureUnhealthy, service)
		}
		if strings.ToLower(process.State) != "running" || strings.ToLower(process.Health) != "healthy" {
			return fmt.Errorf("%w：服务 %s state=%q health=%q", ErrInfrastructureUnhealthy, service, process.State, process.Health)
		}
	}
	return nil
}

func decodeComposeProcesses(data []byte) ([]composeProcess, error) {
	var processes []composeProcess
	if err := json.Unmarshal(data, &processes); err == nil {
		return processes, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var process composeProcess
		if err := decoder.Decode(&process); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		processes = append(processes, process)
	}
	if len(processes) == 0 {
		return nil, errors.New("Docker Compose 未返回容器状态")
	}
	return processes, nil
}

func parseAvailableMemory(reader io.Reader) (uint64, error) {
	scanner := bufio.NewScanner(reader)
	var available uint64
	found := false
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "MemAvailable:" {
			continue
		}
		if found || len(fields) != 3 || fields[2] != "kB" {
			return 0, errors.New("/proc/meminfo 中 MemAvailable 格式无效")
		}
		kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kilobytes > math.MaxUint64/1024 {
			return 0, errors.New("/proc/meminfo 中 MemAvailable 数值无效")
		}
		available = kilobytes * 1024
		found = true
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("读取 /proc/meminfo：%w", err)
	}
	if !found {
		return 0, errors.New("/proc/meminfo 缺少 MemAvailable")
	}
	return available, nil
}
