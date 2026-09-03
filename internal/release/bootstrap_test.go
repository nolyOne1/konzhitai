package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBootstrapDockerHostInspectsAndTagsEveryServiceIndependently(t *testing.T) {
	ids := map[string]string{
		"yunling-api-1":       "sha256:" + repeatHex("1"),
		"yunling-scheduler-1": "sha256:" + repeatHex("2"),
		"yunling-web-1":       "sha256:" + repeatHex("3"),
		"yunling-ops-1":       "sha256:" + repeatHex("4"),
	}
	runner := &bootstrapDockerRunner{imageIDs: ids}
	host := NewDockerBootstrapHost(runner, "yunling-api-1", "yunling_agent_releases")
	images, err := host.CaptureAndTagImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := ServiceImages{
		API: "yunling-local-bootstrap/api:111111111111", Scheduler: "yunling-local-bootstrap/scheduler:222222222222",
		Web: "yunling-local-bootstrap/web:333333333333", Ops: "yunling-local-bootstrap/ops:444444444444",
	}
	if !reflect.DeepEqual(images, want) {
		t.Fatalf("镜像标签不匹配：got=%+v want=%+v", images, want)
	}
	if len(runner.inspectContainers) != 4 || len(runner.tagPairs) != 4 {
		t.Fatalf("必须独立检查并标记四个服务：inspect=%v tags=%v", runner.inspectContainers, runner.tagPairs)
	}
	if runner.inspectContainers[0] != "yunling-api-1" || runner.inspectContainers[1] != "yunling-scheduler-1" {
		t.Fatalf("API 与调度器不得合并检查：%v", runner.inspectContainers)
	}
}

func TestBootstrapDockerHostPublishesOnlyAfterTwoReadOnlyVerifications(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "agent")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "verified.txt"), []byte("locked"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newBootstrapVolumeRunner(t, root)
	runner.imageIDs = map[string]string{
		"yunling-api-1": "sha256:" + repeatHex("1"), "yunling-scheduler-1": "sha256:" + repeatHex("2"),
		"yunling-web-1": "sha256:" + repeatHex("3"), "yunling-ops-1": "sha256:" + repeatHex("4"),
	}
	host := NewDockerBootstrapHost(runner, "yunling-api-1", "yunling_agent_releases")
	if _, err := host.CaptureAndTagImages(context.Background()); err != nil {
		t.Fatal(err)
	}
	verificationCalls := 0
	if err := host.PublishAgentVolume(context.Background(), source, func(path string) error {
		verificationCalls++
		body, err := os.ReadFile(filepath.Join(path, "verified.txt"))
		if err != nil || string(body) != "locked" {
			return errors.New("volume content mismatch")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if verificationCalls != 2 {
		t.Fatalf("临时卷与正式卷必须分别校验：calls=%d", verificationCalls)
	}
	if runner.readOnlyMounts < 2 {
		t.Fatalf("卷校验必须只读挂载：mounts=%d", runner.readOnlyMounts)
	}
	body, err := os.ReadFile(filepath.Join(runner.volumes["yunling_agent_releases"], "verified.txt"))
	if err != nil || string(body) != "locked" {
		t.Fatalf("正式卷内容错误：body=%q err=%v", body, err)
	}
	for volume := range runner.volumes {
		if volume != "yunling_agent_releases" {
			t.Fatalf("成功后不得残留临时卷：%s", volume)
		}
	}
}

func TestBootstrapDockerHostNeverOverwritesNonMatchingExistingVolume(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "agent")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "verified.txt"), []byte("locked"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newBootstrapVolumeRunner(t, root)
	runner.imageIDs = map[string]string{
		"yunling-api-1": "sha256:" + repeatHex("1"), "yunling-scheduler-1": "sha256:" + repeatHex("2"),
		"yunling-web-1": "sha256:" + repeatHex("3"), "yunling-ops-1": "sha256:" + repeatHex("4"),
	}
	existing := filepath.Join(root, "existing-volume")
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "foreign.txt"), []byte("keep-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner.volumes["yunling_agent_releases"] = existing
	host := NewDockerBootstrapHost(runner, "yunling-api-1", "yunling_agent_releases")
	if _, err := host.CaptureAndTagImages(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := host.PublishAgentVolume(context.Background(), source, func(path string) error {
		body, readErr := os.ReadFile(filepath.Join(path, "verified.txt"))
		if readErr != nil || string(body) != "locked" {
			return errors.New("not the locked release")
		}
		return nil
	})
	if !errors.Is(err, ErrBootstrapConflict) {
		t.Fatalf("现有非匹配卷必须拒绝：%v", err)
	}
	body, readErr := os.ReadFile(filepath.Join(existing, "foreign.txt"))
	if readErr != nil || string(body) != "keep-me" {
		t.Fatalf("现有卷被改写：body=%q err=%v", body, readErr)
	}
}

func TestBootstrapImportsVerifiedBaselineAndRefusesSecondImport(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "migrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "migrations", "001.sql"), []byte("select 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(root, "deploy", "docker-compose.yml")
	if err := os.MkdirAll(filepath.Dir(composePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentSource := filepath.Join(root, "source-agent")
	if err := os.MkdirAll(agentSource, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := writeTestAgentRelease(t, agentSource)
	lockData, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, "deploy", "agent", "release-lock.json")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, append(lockData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	images := ServiceImages{
		API: "yunling-local-bootstrap/api:111111111111", Scheduler: "yunling-local-bootstrap/scheduler:222222222222",
		Web: "yunling-local-bootstrap/web:333333333333", Ops: "yunling-local-bootstrap/ops:444444444444",
	}
	host := &fakeBootstrapHost{images: images, agentSource: agentSource}
	store := NewStateStore(filepath.Join(root, "releases"))
	overridePath := filepath.Join(root, "deploy", "docker-compose.release.yml")
	bootstrapper := &Bootstrapper{
		RootDir: root, ComposeFile: composePath, OverrideFile: overridePath,
		AgentLockPath: lockPath, Store: store, Host: host, Locker: bootstrapTestLocker{},
		Now: func() time.Time { return time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC) },
	}
	if err := bootstrapper.Run(context.Background()); err != nil {
		t.Fatalf("首次导入失败：%v", err)
	}
	current, err := store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if current.TargetID != "bootstrap" || current.Origin != OriginBootstrap || !reflect.DeepEqual(current.Images, images) {
		t.Fatalf("基线状态不匹配：%+v", current)
	}
	if current.Compatibility.AgentVersion != "0.1.0" || current.Compatibility.AgentManifestSHA256 != lock.ManifestSHA256 ||
		current.Compatibility.MigrationTreeSHA256 == "" || current.Compatibility.DeploymentContractSHA256 == "" {
		t.Fatalf("基线兼容性不完整：%+v", current.Compatibility)
	}
	wantOverride, err := RenderComposeOverride(current)
	if err != nil {
		t.Fatal(err)
	}
	gotOverride, err := os.ReadFile(overridePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotOverride, wantOverride) || host.publishCalls != 1 {
		t.Fatalf("发布覆盖或代理卷不匹配：publish=%d override=%q", host.publishCalls, gotOverride)
	}

	if err := bootstrapper.Run(context.Background()); !errors.Is(err, ErrBootstrapExists) {
		t.Fatalf("第二次导入必须拒绝：%v", err)
	}
	if host.captureCalls != 1 || host.publishCalls != 1 {
		t.Fatalf("第二次导入不得触碰 Docker：capture=%d publish=%d", host.captureCalls, host.publishCalls)
	}
}

func TestBootstrapVerifiesCopiedAgentBeforePublishingVolume(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "migrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "migrations", "001.sql"), []byte("select 1;"), 0o600); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(root, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	validAgent := filepath.Join(root, "valid-agent")
	if err := os.MkdirAll(validAgent, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := writeTestAgentRelease(t, validAgent)
	lockData, _ := json.Marshal(lock)
	lockPath := filepath.Join(root, "release-lock.json")
	if err := os.WriteFile(lockPath, append(lockData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	badAgent := filepath.Join(root, "bad-agent")
	if err := copyDirectoryFiles(validAgent, badAgent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badAgent, lock.Artifacts[0].FileName), []byte("drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := &fakeBootstrapHost{images: ServiceImages{
		API: "yunling-local-bootstrap/api:111111111111", Scheduler: "yunling-local-bootstrap/scheduler:222222222222",
		Web: "yunling-local-bootstrap/web:333333333333", Ops: "yunling-local-bootstrap/ops:444444444444",
	}, agentSource: badAgent}
	bootstrapper := &Bootstrapper{
		RootDir: root, ComposeFile: composePath, OverrideFile: filepath.Join(root, "override.yml"),
		AgentLockPath: lockPath, Store: NewStateStore(filepath.Join(root, "releases")),
		Host: host, Locker: bootstrapTestLocker{}, Now: time.Now,
	}
	if err := bootstrapper.Run(context.Background()); !errors.Is(err, ErrAgentReleaseDrift) {
		t.Fatalf("漂移代理必须失败：%v", err)
	}
	if host.publishCalls != 0 {
		t.Fatal("代理校验失败前不得创建或修改正式卷")
	}
}

type fakeBootstrapHost struct {
	images       ServiceImages
	agentSource  string
	captureCalls int
	publishCalls int
}

func (host *fakeBootstrapHost) CaptureAndTagImages(context.Context) (ServiceImages, error) {
	host.captureCalls++
	return host.images, nil
}

func (host *fakeBootstrapHost) CopyAgentRelease(_ context.Context, destination string) error {
	return copyDirectoryFiles(host.agentSource, destination)
}

func (host *fakeBootstrapHost) PublishAgentVolume(_ context.Context, source string, verify func(string) error) error {
	host.publishCalls++
	verification := filepath.Join(filepath.Dir(source), "volume-verification")
	if err := copyDirectoryFiles(source, verification); err != nil {
		return err
	}
	return verify(verification)
}

type bootstrapTestLocker struct{}

func (bootstrapTestLocker) TryLock(string) (func() error, error) {
	return func() error { return nil }, nil
}

func copyDirectoryFiles(source, destination string) error {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), body, 0o600); err != nil {
			return err
		}
	}
	return nil
}

type bootstrapDockerRunner struct {
	imageIDs          map[string]string
	inspectContainers []string
	tagPairs          [][2]string
}

func (runner *bootstrapDockerRunner) Run(_ context.Context, name string, args []string, _ []byte) (CommandResult, error) {
	if name != "docker" {
		return CommandResult{}, fmt.Errorf("unexpected command %s", name)
	}
	if len(args) == 6 && args[0] == "inspect" && args[1] == "--type" && args[2] == "container" && args[3] == "--format" && args[4] == "{{.Image}}" {
		container := args[5]
		runner.inspectContainers = append(runner.inspectContainers, container)
		id, ok := runner.imageIDs[container]
		if !ok {
			return CommandResult{ExitCode: 1}, errors.New("container missing")
		}
		return CommandResult{Stdout: []byte(id + "\n")}, nil
	}
	if len(args) == 4 && args[0] == "image" && args[1] == "tag" {
		runner.tagPairs = append(runner.tagPairs, [2]string{args[2], args[3]})
		return CommandResult{}, nil
	}
	return CommandResult{}, fmt.Errorf("unexpected docker args %v", args)
}

func repeatHex(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result
}

type bootstrapVolumeRunner struct {
	t                 *testing.T
	root              string
	imageIDs          map[string]string
	inspectContainers []string
	tagPairs          [][2]string
	volumes           map[string]string
	volumeLabels      map[string]string
	helpers           map[string]string
	readOnlyMounts    int
}

func newBootstrapVolumeRunner(t *testing.T, root string) *bootstrapVolumeRunner {
	t.Helper()
	return &bootstrapVolumeRunner{t: t, root: root, volumes: map[string]string{}, volumeLabels: map[string]string{}, helpers: map[string]string{}}
}

func (runner *bootstrapVolumeRunner) Run(ctx context.Context, name string, args []string, stdin []byte) (CommandResult, error) {
	base := bootstrapDockerRunner{imageIDs: runner.imageIDs, inspectContainers: runner.inspectContainers, tagPairs: runner.tagPairs}
	if len(args) > 0 && (args[0] == "inspect" || args[0] == "image") {
		result, err := base.Run(ctx, name, args, stdin)
		runner.inspectContainers, runner.tagPairs = base.inspectContainers, base.tagPairs
		return result, err
	}
	if name != "docker" || len(args) == 0 {
		return CommandResult{}, fmt.Errorf("unexpected command %s %v", name, args)
	}
	switch args[0] {
	case "volume":
		switch args[1] {
		case "ls":
			if _, exists := runner.volumes["yunling_agent_releases"]; exists {
				return CommandResult{Stdout: []byte("yunling_agent_releases\n")}, nil
			}
			return CommandResult{}, nil
		case "create":
			volume := args[len(args)-1]
			path := filepath.Join(runner.root, "volume-"+volume)
			if err := os.MkdirAll(path, 0o700); err != nil {
				return CommandResult{}, err
			}
			runner.volumes[volume] = path
			for index := 2; index < len(args)-1; index++ {
				if args[index] == "--label" && index+1 < len(args) {
					label := args[index+1]
					if strings.HasPrefix(label, "yunling.bootstrap.install=") {
						runner.volumeLabels[volume] = strings.TrimPrefix(label, "yunling.bootstrap.install=")
					}
				}
			}
			return CommandResult{Stdout: []byte(volume + "\n")}, nil
		case "inspect":
			volume := args[len(args)-1]
			return CommandResult{Stdout: []byte(runner.volumeLabels[volume] + "\n")}, nil
		case "rm":
			volume := args[len(args)-1]
			if path := runner.volumes[volume]; path != "" {
				_ = os.RemoveAll(path)
				delete(runner.volumes, volume)
				delete(runner.volumeLabels, volume)
			}
			return CommandResult{}, nil
		}
	case "create":
		helper, mount := "", ""
		for index := 1; index < len(args)-1; index++ {
			switch args[index] {
			case "--name":
				helper = args[index+1]
			case "--mount":
				mount = args[index+1]
			}
		}
		parts := strings.Split(mount, ",")
		volume := ""
		for _, part := range parts {
			if strings.HasPrefix(part, "src=") {
				volume = strings.TrimPrefix(part, "src=")
			}
			if part == "readonly" {
				runner.readOnlyMounts++
			}
		}
		if helper == "" || runner.volumes[volume] == "" {
			return CommandResult{}, fmt.Errorf("invalid helper mount: %v", args)
		}
		runner.helpers[helper] = volume
		return CommandResult{Stdout: []byte(helper + "\n")}, nil
	case "cp":
		from, to := args[1], args[2]
		if separator := strings.Index(to, ":/release"); separator > 0 {
			helper := to[:separator]
			return CommandResult{}, copyDirectoryFiles(strings.TrimSuffix(filepath.Clean(from), string(os.PathSeparator)+"."), runner.volumes[runner.helpers[helper]])
		}
		if separator := strings.Index(from, ":/release"); separator > 0 {
			helper := from[:separator]
			return CommandResult{}, copyDirectoryFiles(runner.volumes[runner.helpers[helper]], filepath.Clean(to))
		}
	case "rm":
		helper := args[len(args)-1]
		delete(runner.helpers, helper)
		return CommandResult{}, nil
	}
	return CommandResult{}, fmt.Errorf("unexpected docker args %v", args)
}
