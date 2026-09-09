package release

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in: uses only a uniquely named disposable volume and stopped helpers.
func TestBootstrapRealDockerCopiesContentsIntoVolumeRoot(t *testing.T) {
	if os.Getenv("YUNLING_TEST_DOCKER") != "1" {
		t.Skip("set YUNLING_TEST_DOCKER=1 to require real Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker := func(args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, output)
		}
	}
	docker("pull", "alpine:3.23.3")
	token, err := randomDockerSuffix()
	if err != nil {
		t.Fatal(err)
	}
	volume := "yunling-copy-test-" + token
	docker("volume", "create", volume)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if output, err := exec.CommandContext(cleanupCtx, "docker", "volume", "rm", volume).CombinedOutput(); err != nil {
			t.Errorf("cleanup volume: %v: %s", err, output)
		}
	})
	source := filepath.Join(t.TempDir(), "agent")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "manifest.json"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := NewDockerBootstrapHost(NewCommandRunner(), "", "")
	host.apiImageID = "alpine:3.23.3"
	if err := host.populateVolume(ctx, volume, source, false); err != nil {
		t.Fatal(err)
	}
	if err := host.verifyVolume(ctx, volume, func(root string) error {
		body, err := os.ReadFile(filepath.Join(root, "manifest.json"))
		if err != nil {
			return err
		}
		if string(body) != "fixture" {
			t.Fatalf("unexpected content: %q", body)
		}
		entries, err := os.ReadDir(root)
		if err == nil && len(entries) != 1 {
			t.Fatalf("unexpected nested content: %v", entries)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Characterize Compose's project prefix using its real parser, then publish
	// through the same host implementation and read through a Compose consumer.
	project := "yunling-compose-test-" + token
	composeFile := filepath.Join(t.TempDir(), "compose.yml")
	envFile := filepath.Join(filepath.Dir(composeFile), ".env")
	compose := "services:\n  api:\n    image: alpine:3.23.3\n    entrypoint: [cat, /opt/yunling/releases/agent/manifest.json]\n    volumes:\n      - yunling_agent_releases:/opt/yunling/releases/agent:ro\nvolumes:\n  yunling_agent_releases:\n"
	if err := os.WriteFile(composeFile, []byte(compose), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	composeHost := NewDockerBootstrapHost(NewCommandRunner(), "", "yunling_agent_releases", HostConfig{
		ProjectName: project, EnvFile: envFile, ComposeFile: composeFile,
	})
	if err := composeHost.resolveAgentVolume(ctx); err != nil {
		t.Fatal(err)
	}
	wantVolume := project + "_yunling_agent_releases"
	if composeHost.agentVolume != wantVolume {
		t.Fatalf("resolved %q, want %q", composeHost.agentVolume, wantVolume)
	}
	composeHost.apiImageID = "alpine:3.23.3"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if output, err := exec.CommandContext(cleanupCtx, "docker", "compose", "-p", project, "--env-file", envFile, "-f", composeFile, "down", "--volumes").CombinedOutput(); err != nil {
			t.Errorf("cleanup test Compose project: %v: %s", err, output)
		}
	})
	if err := composeHost.PublishAgentVolume(ctx, source, func(root string) error {
		_, err := os.ReadFile(filepath.Join(root, "manifest.json"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(ctx, "docker", "compose", "-p", project, "--env-file", envFile, "-f", composeFile, "run", "--rm", "--no-deps", "-T", "api").Output()
	if err != nil || string(output) != "fixture" {
		t.Fatalf("Compose API cannot read published manifest: %q %v", output, err)
	}
}
