package release

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBootstrapPublishesComposePrefixedVolume(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "manifest.json"), []byte("verified"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := newBootstrapVolumeRunner(t, root)
	host := NewDockerBootstrapHost(runner, "yunling-api-1", "yunling_yunling_agent_releases")
	host.apiImageID = "sha256:" + repeatHex("1")
	err := host.PublishAgentVolume(context.Background(), source, func(dir string) error {
		_, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(runner.volumes["yunling_yunling_agent_releases"], "manifest.json"))
	if err != nil || string(body) != "verified" {
		t.Fatalf("published content=%q err=%v", body, err)
	}
	if _, exists := runner.volumes["yunling_agent_releases"]; exists {
		t.Fatal("published to unused unprefixed volume")
	}
}

type composeBootstrapRunner struct {
	inner      *bootstrapVolumeRunner
	configJSON string
	calls      int
}

func (r *composeBootstrapRunner) Run(ctx context.Context, name string, args []string, stdin []byte) (CommandResult, error) {
	if len(args) > 2 && args[0] == "volume" && args[1] == "create" && !strings.Contains(args[len(args)-1], "_stage_") {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--label com.docker.compose.project=yunling") || !strings.Contains(joined, "--label com.docker.compose.volume=yunling_agent_releases") {
			return CommandResult{}, fmt.Errorf("missing Compose ownership labels")
		}
	}
	if len(args) > 0 && args[0] == "compose" {
		want := []string{"compose", "--project-name", "yunling", "--env-file", "/deploy/.env", "-f", "/deploy/compose.yml", "config", "--format", "json"}
		if name != "docker" || !reflect.DeepEqual(args, want) {
			return CommandResult{}, fmt.Errorf("unexpected compose arguments: %v", args)
		}
		r.calls++
		return CommandResult{Stdout: []byte(r.configJSON)}, nil
	}
	return r.inner.Run(ctx, name, args, stdin)
}

func TestBootstrapResolvesComposeMountBeforeMutatingDocker(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"project prefix", `{"services":{"api":{"volumes":[{"type":"volume","source":"yunling_agent_releases","target":"/opt/yunling/releases/agent","read_only":true}]}},"volumes":{"yunling_agent_releases":{"name":"yunling_yunling_agent_releases"}}}`, "yunling_yunling_agent_releases"},
		{"explicit name", `{"services":{"api":{"volumes":[{"type":"volume","source":"yunling_agent_releases","target":"/opt/yunling/releases/agent","read_only":true}]}},"volumes":{"yunling_agent_releases":{"name":"custom.agent-volume"}}}`, "custom.agent-volume"},
		{"missing mount", `{"services":{"api":{}},"volumes":{}}`, ""},
		{"writable mount", `{"services":{"api":{"volumes":[{"type":"volume","source":"yunling_agent_releases","target":"/opt/yunling/releases/agent"}]}},"volumes":{"yunling_agent_releases":{"name":"wrong"}}}`, ""},
		{"malformed", `not json`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := newBootstrapVolumeRunner(t, t.TempDir())
			inner.imageIDs = map[string]string{"yunling-api-1": "sha256:" + repeatHex("1"), "yunling-scheduler-1": "sha256:" + repeatHex("2"), "yunling-web-1": "sha256:" + repeatHex("3"), "yunling-ops-1": "sha256:" + repeatHex("4")}
			runner := &composeBootstrapRunner{inner: inner, configJSON: tc.body}
			host := NewDockerBootstrapHost(runner, "yunling-api-1", "yunling_agent_releases", HostConfig{ProjectName: "yunling", EnvFile: "/deploy/.env", ComposeFile: "/deploy/compose.yml"})
			_, err := host.CaptureAndTagImages(context.Background())
			if tc.want == "" {
				if err == nil || len(inner.tagPairs) != 0 || len(inner.volumes) != 0 {
					t.Fatalf("invalid config mutated Docker: err=%v", err)
				}
			} else if err != nil || host.agentVolume != tc.want {
				t.Fatalf("volume=%q err=%v", host.agentVolume, err)
			}
			if tc.want != "" {
				source := t.TempDir()
				if err := os.WriteFile(filepath.Join(source, "manifest.json"), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				verify := func(dir string) error { _, err := os.ReadFile(filepath.Join(dir, "manifest.json")); return err }
				if err := host.PublishAgentVolume(context.Background(), source, verify); err != nil {
					t.Fatal(err)
				}
				if err := host.PublishAgentVolume(context.Background(), source, verify); err != nil {
					t.Fatalf("repeat publish: %v", err)
				}
			}
			if runner.calls != 1 {
				t.Fatalf("compose resolution calls=%d", runner.calls)
			}
		})
	}
}
