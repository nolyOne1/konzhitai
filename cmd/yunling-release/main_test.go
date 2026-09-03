package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/release"
)

func TestExecuteReadsOneStrictRequestAndWritesOneResult(t *testing.T) {
	request := validCLIRequest()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	calls := 0
	code := run([]string{"execute"}, bytes.NewReader(body), stdout, stderr, dependencies{
		execute: func(_ context.Context, got release.Request) (release.Result, error) {
			calls++
			if got.TargetID != request.TargetID || got.Actor != request.Actor {
				t.Fatalf("执行请求不匹配：got=%+v want=%+v", got, request)
			}
			return release.Result{
				Operation: got.Operation, TargetID: got.TargetID, Actor: got.Actor,
				WorkflowRunID: got.WorkflowRunID, WorkflowURL: got.WorkflowURL,
				Status: "succeeded", RollbackStatus: "not-required",
				StartedAt:  time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
				FinishedAt: time.Date(2026, 9, 3, 12, 1, 0, 0, time.UTC),
			}, nil
		},
	})
	if code != 0 || calls != 1 {
		t.Fatalf("退出码=%d calls=%d stderr=%s", code, calls, stderr)
	}
	var result release.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || bytes.Count(stdout.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("标准输出必须只有一个结果对象：%q", stdout)
	}
	if strings.Contains(stdout.String(), "secret") {
		t.Fatal("标准输出泄露测试秘密")
	}
}

func TestExecuteRejectsOversizedOrTrailingJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "超过 256 KiB", body: strings.Repeat("x", 256<<10+1)},
		{name: "尾随 JSON", body: string(mustJSON(t, validCLIRequest())) + ` {"operation":"rollback"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			code := run([]string{"execute"}, strings.NewReader(test.body), stdout, stderr, dependencies{
				execute: func(context.Context, release.Request) (release.Result, error) {
					called = true
					return release.Result{}, errors.New("不应调用")
				},
			})
			if code == 0 || called || stdout.Len() != 0 {
				t.Fatalf("危险输入未被拒绝：code=%d called=%v stdout=%q stderr=%q", code, called, stdout, stderr)
			}
		})
	}
}

func TestExecuteRealModeRequiresRootAndIgnoresSSHOriginalCommand(t *testing.T) {
	t.Setenv("SSH_ORIGINAL_COMMAND", "docker compose down --volumes")
	request := validCLIRequest()
	called := false
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	code := run([]string{"execute"}, bytes.NewReader(mustJSON(t, request)), stdout, stderr, dependencies{
		requireRoot: true,
		isRoot:      func() bool { return false },
		execute: func(context.Context, release.Request) (release.Result, error) {
			called = true
			return release.Result{}, nil
		},
	})
	if code == 0 || called || stdout.Len() != 0 || !strings.Contains(stderr.String(), "root") {
		t.Fatalf("非 root 必须被拒绝：code=%d called=%v stdout=%q stderr=%q", code, called, stdout, stderr)
	}
}

func TestBootstrapRequiresRealRootAndRunsExactlyOnce(t *testing.T) {
	tests := []struct {
		name      string
		isRoot    bool
		args      []string
		wantCode  int
		wantCalls int
	}{
		{name: "root 可导入", isRoot: true, args: []string{"bootstrap"}, wantCode: 0, wantCalls: 1},
		{name: "非 root 拒绝", isRoot: false, args: []string{"bootstrap"}, wantCode: 1},
		{name: "拒绝额外参数", isRoot: true, args: []string{"bootstrap", "again"}, wantCode: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			code := run(test.args, strings.NewReader(""), stdout, stderr, dependencies{
				requireRoot: true,
				isRoot:      func() bool { return test.isRoot },
				bootstrap: func(context.Context) error {
					calls++
					return nil
				},
			})
			if code != test.wantCode || calls != test.wantCalls || stdout.Len() != 0 {
				t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, calls, stdout, stderr)
			}
		})
	}
}

func TestCandidateAuthorizeValidatesBoundedGitHubEventFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow-run.json")
	trusted := `{"name":"云令 CI","conclusion":"success","head_branch":"main","event":"push","repository":{"id":1354623243}}`
	if err := os.WriteFile(path, []byte(trusted), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	code := run([]string{"candidate", "authorize", "--input", path, "--repository-id", "1354623243"}, strings.NewReader(""), stdout, stderr, dependencies{})
	if code != 0 || stdout.Len() != 0 {
		t.Fatalf("可信候选运行授权失败：code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(trusted, `"head_branch":"main"`, `"head_branch":"feature"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	code = run([]string{"candidate", "authorize", "--input", path, "--repository-id", "1354623243"}, strings.NewReader(""), stdout, stderr, dependencies{})
	if code == 0 || stdout.Len() != 0 {
		t.Fatalf("非主分支候选必须拒绝：code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestManifestCreateComputesCompatibilityAndValidateAcceptsIt(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "release-manifest.json")
	args := []string{
		"manifest", "create",
		"--candidate-run-id", "101",
		"--repository-id", strconv.FormatInt(trustedRepositoryID, 10),
		"--source-sha", strings.Repeat("d", 40),
		"--services", "ghcr.io/nolyone1/yunling-services@sha256:" + strings.Repeat("a", 64),
		"--web", "ghcr.io/nolyone1/yunling-web@sha256:" + strings.Repeat("b", 64),
		"--ops", "ghcr.io/nolyone1/yunling-ops@sha256:" + strings.Repeat("c", 64),
		"--repository-root", repositoryRoot,
		"--agent-lock", "deploy/agent/release-lock.json",
		"--output", outputPath,
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	code := run(args, strings.NewReader(""), stdout, stderr, dependencies{
		now: func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) },
	})
	if code != 0 || stdout.Len() != 0 {
		t.Fatalf("清单生成失败：code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	file, err := os.Open(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := release.DecodeManifest(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Compatibility.MigrationTreeSHA256 == "" || manifest.Compatibility.DeploymentContractSHA256 == "" ||
		manifest.Compatibility.AgentVersion != "0.1.0" || manifest.Compatibility.AgentManifestSHA256 == "" {
		t.Fatalf("兼容性摘要不完整：%+v", manifest.Compatibility)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(outputPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("清单权限=%o，期望=600", info.Mode().Perm())
		}
	}
	stderr.Reset()
	code = run([]string{
		"manifest", "validate", "--input", outputPath,
		"--repository-id", strconv.FormatInt(trustedRepositoryID, 10), "--owner", "nolyone1",
	}, strings.NewReader(""), stdout, stderr, dependencies{})
	if code != 0 || !strings.Contains(stderr.String(), "候选清单有效") {
		t.Fatalf("清单复核失败：code=%d stderr=%q", code, stderr)
	}
}

func TestRequestCreateRequiresManifestOnlyForDeploy(t *testing.T) {
	manifest := validCLIRequest().Manifest
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := writePrivateJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
	}{
		{name: "部署", args: []string{
			"request", "create", "--operation", "deploy", "--target-id", "101",
			"--actor", "nolyOne1", "--workflow-run-id", "123",
			"--workflow-url", "https://github.com/nolyOne1/konzhitai/actions/runs/123",
			"--manifest", manifestPath,
		}},
		{name: "回滚", args: []string{
			"request", "create", "--operation", "rollback", "--target-id", "bootstrap",
			"--actor", "nolyOne1", "--workflow-run-id", "123",
			"--workflow-url", "https://github.com/nolyOne1/konzhitai/actions/runs/123",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			code := run(test.args, strings.NewReader(""), stdout, stderr, dependencies{})
			if code != 0 {
				t.Fatalf("请求生成失败：code=%d stderr=%q", code, stderr)
			}
			request, err := release.DecodeRequest(bytes.NewReader(stdout.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "部署" && request.Manifest == nil {
				t.Fatal("部署请求必须携带清单")
			}
			if test.name == "回滚" && request.Manifest != nil {
				t.Fatal("回滚请求不得携带清单")
			}
		})
	}
}

func validCLIRequest() release.Request {
	manifest := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion, CandidateRunID: 101, RepositoryID: trustedRepositoryID,
		SourceSHA: strings.Repeat("d", 40), CreatedAt: time.Date(2026, 9, 3, 11, 0, 0, 0, time.UTC),
		Images: release.Images{
			Services: "ghcr.io/nolyone1/yunling-services@sha256:" + strings.Repeat("a", 64),
			Web:      "ghcr.io/nolyone1/yunling-web@sha256:" + strings.Repeat("b", 64),
			Ops:      "ghcr.io/nolyone1/yunling-ops@sha256:" + strings.Repeat("c", 64),
		},
		Compatibility: release.Compatibility{
			MigrationTreeSHA256: strings.Repeat("1", 64), DeploymentContractSHA256: strings.Repeat("2", 64),
			AgentVersion: "0.1.0", AgentManifestSHA256: strings.Repeat("3", 64),
		},
	}
	return release.Request{
		Operation: release.OperationDeploy, TargetID: "101", Actor: "nolyOne1",
		WorkflowRunID: 123, WorkflowURL: "https://github.com/nolyOne1/konzhitai/actions/runs/123",
		Manifest: &manifest,
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
