package release

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	ErrUnsafeWorkflow  = errors.New("GitHub 工作流安全策略不满足")
	immutableActionRef = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$`)
	sshCommandPattern  = regexp.MustCompile(`(^|[^a-z0-9_-])(ssh|scp|sftp)([^a-z0-9_-]|$)`)
)

const maxWorkflowBytes = 1 << 20

type workflowDocument struct {
	Name        string                     `yaml:"name"`
	On          map[string]workflowTrigger `yaml:"on"`
	Permissions map[string]string          `yaml:"permissions"`
	Concurrency workflowConcurrency        `yaml:"concurrency"`
	Jobs        map[string]workflowJob     `yaml:"jobs"`
}

type workflowTrigger struct {
	Workflows []string `yaml:"workflows"`
	Types     []string `yaml:"types"`
}

type workflowConcurrency struct {
	Group            string `yaml:"group"`
	CancelInProgress bool   `yaml:"cancel-in-progress"`
}

type workflowJob struct {
	If          string            `yaml:"if"`
	RunsOn      string            `yaml:"runs-on"`
	Environment any               `yaml:"environment"`
	Permissions map[string]string `yaml:"permissions"`
	Env         map[string]any    `yaml:"env"`
	Steps       []workflowStep    `yaml:"steps"`
}

type workflowStep struct {
	ID   string         `yaml:"id"`
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	Env  map[string]any `yaml:"env"`
	With map[string]any `yaml:"with"`
}

func ValidateWorkflowFiles(repositoryRoot string) error {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil || repositoryRoot == "" {
		return fmt.Errorf("%w：仓库根目录无效", ErrUnsafeWorkflow)
	}
	path := filepath.Join(root, ".github", "workflows", "publish-candidate.yml")
	workflow, err := loadWorkflow(path)
	if err != nil {
		return err
	}
	return validateCandidateWorkflow(workflow)
}

func loadWorkflow(path string) (workflowDocument, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxWorkflowBytes {
		return workflowDocument{}, fmt.Errorf("%w：工作流必须是小于 1 MiB 的普通文件", ErrUnsafeWorkflow)
	}
	file, err := os.Open(path)
	if err != nil {
		return workflowDocument{}, fmt.Errorf("%w：打开工作流：%v", ErrUnsafeWorkflow, err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(io.LimitReader(file, maxWorkflowBytes+1))
	var workflow workflowDocument
	if err := decoder.Decode(&workflow); err != nil {
		return workflowDocument{}, fmt.Errorf("%w：解析工作流：%v", ErrUnsafeWorkflow, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return workflowDocument{}, fmt.Errorf("%w：工作流包含多个 YAML 文档", ErrUnsafeWorkflow)
	}
	return workflow, nil
}

func validateCandidateWorkflow(workflow workflowDocument) error {
	if workflow.Name != "云令候选版本" || len(workflow.On) != 1 {
		return ErrUnsafeWorkflow
	}
	trigger, ok := workflow.On["workflow_run"]
	if !ok || !equalStrings(trigger.Workflows, []string{"云令 CI"}) || !equalStrings(trigger.Types, []string{"completed"}) {
		return ErrUnsafeWorkflow
	}
	if !equalPermissions(workflow.Permissions, map[string]string{"contents": "read"}) ||
		workflow.Concurrency.Group != "candidate-${{ github.event.workflow_run.head_sha }}" || !workflow.Concurrency.CancelInProgress {
		return ErrUnsafeWorkflow
	}
	if len(workflow.Jobs) != 1 {
		return ErrUnsafeWorkflow
	}
	job, ok := workflow.Jobs["publish"]
	if !ok || job.RunsOn != "ubuntu-latest" || job.Environment != nil {
		return ErrUnsafeWorkflow
	}
	if !equalPermissions(job.Permissions, map[string]string{
		"contents": "read", "packages": "write", "id-token": "write", "attestations": "write",
	}) {
		return ErrUnsafeWorkflow
	}
	for _, guard := range []string{
		"github.event.workflow_run.conclusion == 'success'",
		"github.event.workflow_run.head_branch == 'main'",
		"github.event.workflow_run.event == 'push'",
		"github.event.workflow_run.repository.id == github.repository_id",
	} {
		if !strings.Contains(job.If, guard) {
			return ErrUnsafeWorkflow
		}
	}
	if len(job.Steps) == 0 {
		return ErrUnsafeWorkflow
	}
	for _, step := range job.Steps {
		if step.Uses != "" {
			if !immutableActionRef.MatchString(step.Uses) || strings.Contains(strings.ToLower(step.Uses), "ssh") {
				return ErrUnsafeWorkflow
			}
		}
		run := strings.ToLower(step.Run)
		if sshCommandPattern.MatchString(run) || strings.Contains(run, "aiwise.top") || strings.Contains(run, "production") {
			return ErrUnsafeWorkflow
		}
		if containsCandidateProductionReference(step.Env) {
			return ErrUnsafeWorkflow
		}
		for _, value := range step.With {
			text := strings.ToLower(fmt.Sprint(value))
			if strings.Contains(text, "yunling-agent") || strings.Contains(text, "aiwise.top") || strings.Contains(text, "production") || strings.Contains(text, "ssh") {
				return ErrUnsafeWorkflow
			}
		}
	}
	if containsCandidateProductionReference(job.Env) {
		return ErrUnsafeWorkflow
	}
	if err := validateCandidateCheckoutAndAuthorization(job.Steps); err != nil {
		return err
	}
	if err := validateCandidateRegistryAndManifest(job.Steps); err != nil {
		return err
	}
	if err := validateCandidateBuilds(job.Steps); err != nil {
		return err
	}
	if err := validateCandidateAttestations(job.Steps); err != nil {
		return err
	}
	return validateCandidateUpload(job.Steps)
}

func containsCandidateProductionReference(values map[string]any) bool {
	for key, value := range values {
		text := strings.ToLower(key + "=" + fmt.Sprint(value))
		if strings.Contains(text, "production") || strings.Contains(text, "ssh") || strings.Contains(text, "aiwise.top") {
			return true
		}
	}
	return false
}

func validateCandidateRegistryAndManifest(steps []workflowStep) error {
	login := findStepByUse(steps, "docker/login-action@")
	if login == nil || login.Uses != "docker/login-action@f4ef78c080cd8ba55a85445d5b36e214a81df20a" ||
		fmt.Sprint(login.With["registry"]) != "ghcr.io" || fmt.Sprint(login.With["username"]) != "${{ github.actor }}" ||
		fmt.Sprint(login.With["password"]) != "${{ secrets.GITHUB_TOKEN }}" {
		return ErrUnsafeWorkflow
	}
	required := []string{
		"yunling-release manifest create", `--candidate-run-id "${{ github.run_id }}"`,
		`--repository-id "${{ github.repository_id }}"`, `--source-sha "${{ github.event.workflow_run.head_sha }}"`,
		`ghcr.io/nolyone1/yunling-services@${{ steps.build-services.outputs.digest }}`,
		`ghcr.io/nolyone1/yunling-web@${{ steps.build-web.outputs.digest }}`,
		`ghcr.io/nolyone1/yunling-ops@${{ steps.build-ops.outputs.digest }}`,
		"--agent-lock deploy/agent/release-lock.json", "--output release-manifest.json",
		"yunling-release manifest validate --input release-manifest.json",
	}
	for _, step := range steps {
		if !strings.Contains(step.Run, "yunling-release manifest create") {
			continue
		}
		for _, fragment := range required {
			if !strings.Contains(step.Run, fragment) {
				return ErrUnsafeWorkflow
			}
		}
		return nil
	}
	return ErrUnsafeWorkflow
}

func validateCandidateCheckoutAndAuthorization(steps []workflowStep) error {
	checkout := findStepByUse(steps, "actions/checkout@")
	if checkout == nil || fmt.Sprint(checkout.With["ref"]) != "${{ github.event.workflow_run.head_sha }}" {
		return ErrUnsafeWorkflow
	}
	authorized := false
	for _, step := range steps {
		if !strings.Contains(step.Run, "yunling-release candidate authorize") {
			continue
		}
		if !strings.Contains(step.Run, `jq -c '.workflow_run' "$GITHUB_EVENT_PATH"`) ||
			!strings.Contains(step.Run, "wc -c") || !strings.Contains(step.Run, "262144") ||
			!strings.Contains(step.Run, `--repository-id "${{ github.repository_id }}"`) {
			return ErrUnsafeWorkflow
		}
		authorized = true
	}
	if !authorized {
		return ErrUnsafeWorkflow
	}
	return nil
}

func validateCandidateBuilds(steps []workflowStep) error {
	expected := map[string]struct {
		file string
		tag  string
	}{
		"build-services": {file: "deploy/Dockerfile.services", tag: "ghcr.io/nolyone1/yunling-services:sha-${{ github.event.workflow_run.head_sha }}"},
		"build-web":      {file: "deploy/Dockerfile.web", tag: "ghcr.io/nolyone1/yunling-web:sha-${{ github.event.workflow_run.head_sha }}"},
		"build-ops":      {file: "deploy/Dockerfile.ops", tag: "ghcr.io/nolyone1/yunling-ops:sha-${{ github.event.workflow_run.head_sha }}"},
	}
	seen := make(map[string]struct{}, len(expected))
	for _, step := range steps {
		want, ok := expected[step.ID]
		if !ok {
			continue
		}
		if step.Uses != "docker/build-push-action@3b5e8027fcad23fda98b2e3ac259d8d67585f671" ||
			fmt.Sprint(step.With["file"]) != want.file || step.With["push"] != true ||
			fmt.Sprint(step.With["platforms"]) != "linux/amd64" || fmt.Sprint(step.With["tags"]) != want.tag {
			return ErrUnsafeWorkflow
		}
		seen[step.ID] = struct{}{}
	}
	if len(seen) != len(expected) {
		return ErrUnsafeWorkflow
	}
	return nil
}

func validateCandidateAttestations(steps []workflowStep) error {
	expected := map[string]string{
		"ghcr.io/nolyone1/yunling-services": "${{ steps.build-services.outputs.digest }}",
		"ghcr.io/nolyone1/yunling-web":      "${{ steps.build-web.outputs.digest }}",
		"ghcr.io/nolyone1/yunling-ops":      "${{ steps.build-ops.outputs.digest }}",
	}
	seen := make(map[string]struct{}, len(expected))
	bundle := false
	for _, step := range steps {
		if step.Uses != "actions/attest@59d89421af93a897026c735860bf21b6eb4f7b26" {
			continue
		}
		if path := fmt.Sprint(step.With["subject-path"]); path == "yunling-release-bootstrap.tar.gz" {
			bundle = true
			continue
		}
		name := fmt.Sprint(step.With["subject-name"])
		wantDigest, ok := expected[name]
		if !ok || fmt.Sprint(step.With["subject-digest"]) != wantDigest || step.With["push-to-registry"] != true {
			return ErrUnsafeWorkflow
		}
		seen[name] = struct{}{}
	}
	if len(seen) != 3 || !bundle {
		return ErrUnsafeWorkflow
	}
	return nil
}

func validateCandidateUpload(steps []workflowStep) error {
	upload := findStepByUse(steps, "actions/upload-artifact@")
	if upload == nil || fmt.Sprint(upload.With["name"]) != "yunling-release-${{ github.event.workflow_run.head_sha }}" ||
		fmt.Sprint(upload.With["if-no-files-found"]) != "error" {
		return ErrUnsafeWorkflow
	}
	retention, ok := upload.With["retention-days"].(int)
	if !ok || retention != 90 {
		return ErrUnsafeWorkflow
	}
	paths := strings.Fields(fmt.Sprint(upload.With["path"]))
	sort.Strings(paths)
	if !equalStrings(paths, []string{"SHA256SUMS", "release-manifest.json", "yunling-release-bootstrap.tar.gz"}) {
		return ErrUnsafeWorkflow
	}
	return nil
}

func findStepByUse(steps []workflowStep, prefix string) *workflowStep {
	for index := range steps {
		if strings.HasPrefix(steps[index].Uses, prefix) {
			return &steps[index]
		}
	}
	return nil
}

func equalPermissions(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
