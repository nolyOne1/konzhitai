package release

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type commandCall struct {
	name  string
	args  []string
	stdin []byte
}

type recordingRunner struct {
	calls   []commandCall
	results []CommandResult
	errors  []error
}

func (runner *recordingRunner) Run(_ context.Context, name string, args []string, stdin []byte) (CommandResult, error) {
	runner.calls = append(runner.calls, commandCall{
		name: name, args: append([]string(nil), args...), stdin: append([]byte(nil), stdin...),
	})
	index := len(runner.calls) - 1
	var result CommandResult
	if index < len(runner.results) {
		result = runner.results[index]
	}
	if index < len(runner.errors) {
		return result, runner.errors[index]
	}
	return result, nil
}

type fakeResources struct {
	free      uint64
	memory    uint64
	freeErr   error
	memoryErr error
}

func (resources fakeResources) FreeBytes(string) (uint64, error) {
	return resources.free, resources.freeErr
}

func (resources fakeResources) AvailableMemory() (uint64, error) {
	return resources.memory, resources.memoryErr
}

func TestPreflightStopsBeforeDockerWhenResourcesAreLow(t *testing.T) {
	runner := &recordingRunner{}
	resources := fakeResources{free: 3<<30 - 1, memory: 2 << 30}
	err := Preflight(context.Background(), HostConfig{}, runner, resources)
	if !errors.Is(err, ErrInsufficientDisk) {
		t.Fatalf("实际错误：%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("资源不足时不得调用 Docker")
	}
}

func TestPreflightAcceptsTheDocumented512MiBMemoryFloor(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{},
		{},
		{Stdout: []byte(`[
			{"Service":"postgres","State":"running","Health":"healthy"},
			{"Service":"redis","State":"running","Health":"healthy"},
			{"Service":"minio","State":"running","Health":"healthy"},
			{"Service":"caddy","State":"running","Health":"healthy"}
		]`)},
	}}
	err := Preflight(context.Background(), HostConfig{}, runner, fakeResources{free: 3 << 30, memory: 512 << 20})
	if err != nil {
		t.Fatalf("符合设计的 512 MiB 主机应通过资源门槛：%v", err)
	}
}

func TestPreflightUsesFixedReadOnlyDockerOrder(t *testing.T) {
	runner := &recordingRunner{results: []CommandResult{
		{},
		{},
		{Stdout: []byte(`[
			{"Service":"postgres","State":"running","Health":"healthy"},
			{"Service":"redis","State":"running","Health":"healthy"},
			{"Service":"minio","State":"running","Health":"healthy"},
			{"Service":"caddy","State":"running","Health":"healthy"}
		]`)},
	}}
	config := HostConfig{
		RootDir: "/opt/yunling", ComposeFile: "deploy/docker-compose.yml",
		OverrideFile: "deploy/release.override.yml", EnvFile: "deploy/.env", ProjectName: "yunling",
	}
	resources := fakeResources{free: 4 << 30, memory: 3 << 30}
	if err := Preflight(context.Background(), config, runner, resources); err != nil {
		t.Fatal(err)
	}

	want := []commandCall{
		{name: "docker", args: []string{"version"}},
		{name: "docker", args: []string{"compose", "version"}},
		{name: "docker", args: []string{
			"compose", "--project-name", "yunling", "--env-file", "deploy/.env",
			"-f", "deploy/docker-compose.yml", "-f", "deploy/release.override.yml",
			"ps", "--format", "json", "postgres", "redis", "minio", "caddy",
		}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("Docker 预检顺序不匹配：\ngot=%#v\nwant=%#v", runner.calls, want)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call.args, " ")
		for _, forbidden := range []string{" up ", " down ", " restart ", " migrate ", " volume "} {
			if strings.Contains(" "+joined+" ", forbidden) {
				t.Fatalf("预检出现破坏性命令：%s %s", call.name, joined)
			}
		}
	}
}

func TestPreflightRejectsMissingOrUnhealthyInfrastructure(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
	}{
		{name: "缺少 caddy", stdout: `[
			{"Service":"postgres","State":"running","Health":"healthy"},
			{"Service":"redis","State":"running","Health":"healthy"},
			{"Service":"minio","State":"running","Health":"healthy"}
		]`},
		{name: "redis 正在启动", stdout: `[
			{"Service":"postgres","State":"running","Health":"healthy"},
			{"Service":"redis","State":"running","Health":"starting"},
			{"Service":"minio","State":"running","Health":"healthy"},
			{"Service":"caddy","State":"running","Health":"healthy"}
		]`},
		{name: "postgres 已停止", stdout: `[
			{"Service":"postgres","State":"exited","Health":"healthy"},
			{"Service":"redis","State":"running","Health":"healthy"},
			{"Service":"minio","State":"running","Health":"healthy"},
			{"Service":"caddy","State":"running","Health":"healthy"}
		]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{results: []CommandResult{{}, {}, {Stdout: []byte(test.stdout)}}}
			err := Preflight(context.Background(), HostConfig{}, runner, fakeResources{free: 4 << 30, memory: 3 << 30})
			if !errors.Is(err, ErrInfrastructureUnhealthy) {
				t.Fatalf("实际错误：%v", err)
			}
		})
	}
}

func TestParseAvailableMemoryUsesOnlyMemAvailable(t *testing.T) {
	contents := "MemTotal:       8000000 kB\nMemFree:          10 kB\nMemAvailable:   2097152 kB\nCached:       9999999 kB\n"
	got, err := parseAvailableMemory(strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	if got != 2<<30 {
		t.Fatalf("可用内存=%d，期望=%d", got, uint64(2<<30))
	}
}
