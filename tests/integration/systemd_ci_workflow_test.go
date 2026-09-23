package integration_test

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCISystemdAcceptanceIsIsolatedAndMandatory(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	var parsed struct {
		Jobs map[string]struct {
			RunsOn  string `yaml:"runs-on"`
			Timeout int    `yaml:"timeout-minutes"`
			Steps   []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
		t.Fatal(err)
	}
	job, ok := parsed.Jobs["systemd-isolated"]
	if !ok || job.RunsOn != "ubuntu-latest" || job.Timeout <= 0 || job.Timeout > 20 {
		t.Fatal("acceptance must use a bounded GitHub-hosted VM")
	}
	text := ciJob(t, workflow, "systemd-isolated")
	requireCIText(t, text, "persist-credentials: false", "go test ./internal/task ./internal/scheduler ./internal/store/redis",
		"go test -tags=systemdintegration -c", "./internal/executor", "./cmd/agent",
		"sudo -n env GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=", "bash deploy/agent/systemd_ci_test.sh")
	for _, forbidden := range []string{"continue-on-error", "self-hosted", "secrets.", "environment:", "|| true", "workflow_dispatch"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("unsafe or optional acceptance: %s", forbidden)
		}
	}
	script := readRepoText(t, "deploy/agent/systemd_ci_test.sh")
	requireCIText(t, script, "set -euo pipefail", `"${RUNNER_ENVIRONMENT:-}" == github-hosted`,
		`"$(cat /proc/1/comm)" == systemd`, `[[ ! -e "$path" && ! -L "$path" ]]`,
		"list-unit-files --no-legend", "runuser -u yunling-agent -- env -i", "YUNLING_TEST_SYSTEMD=1",
		"grep -Fx 'TestSystemdIsolatedAcceptance'", "-test.run '^TestSystemdIsolatedAcceptance$'", "-test.timeout=3m",
		"'ExecStart='", "'ExecStart=/usr/bin/sleep infinity'", "systemctl start yunling-agent.service")
	if strings.Index(script, "RUNNER_ENVIRONMENT") > strings.Index(script, "groupadd --system") {
		t.Fatal("guard must precede mutations")
	}
	override := strings.Index(script, ">/etc/systemd/system/yunling-agent.service.d/ci.conf")
	reload := strings.Index(script, "systemctl daemon-reload")
	start := strings.Index(script, "systemctl start yunling-agent.service")
	if override < 0 || reload < override || start < reload {
		t.Fatal("dummy parent override and daemon-reload must precede starting the agent service")
	}
	for _, forbidden := range []string{"rm -rf", "--control-url", "systemctl enable yunling-agent", "curl ", "ssh ", "scp "} {
		if strings.Contains(script, forbidden) {
			t.Errorf("fixture may access production or erase installation: %s", forbidden)
		}
	}
	test := readRepoText(t, "internal/executor/systemd_integration_linux_test.go")
	requireCIText(t, test, "//go:build linux && systemdintegration", "executor.NewSystemdLauncher()",
		"exit_zero", "exit_seven", "cancel_tree", "timeout_tree", "TestSystemdReplayHelper",
		"reflect.DeepEqual", "assertCIChildGone", "ErrExecutionTokenMismatch", "yunling-runner")
	if strings.Contains(test, "t.Skip") {
		t.Fatal("real-systemd acceptance must fail instead of skip")
	}
}
