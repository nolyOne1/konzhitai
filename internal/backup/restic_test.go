package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type resticFakeRunner struct {
	calls         []exportCommandCall
	repositoryNew bool
	snapshots     string
}

func TestResticCopyToCOSUsesDNSRegionRepositoryFilesAndCredentialEnvironment(t *testing.T) {
	directory := t.TempDir()
	runID := uuid.NewString()
	localRepositoryFile := filepath.Join(directory, "local-repository")
	cosRepositoryFile := filepath.Join(directory, "cos-repository")
	resticPasswordFile := filepath.Join(directory, "restic-password")
	secretIDFile := filepath.Join(directory, "cos-secret-id")
	secretKeyFile := filepath.Join(directory, "cos-secret-key")
	for path, value := range map[string]string{
		localRepositoryFile: "/var/lib/yunling-ops/local-repo",
		resticPasswordFile:  "restic-password",
		secretIDFile:        "cos-secret-id",
		secretKeyFile:       "cos-secret-key",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expectedRepository := "s3:https://cos.ap-guangzhou.myqcloud.com/yunling-backup-1250000000/yunling"
	if err := os.WriteFile(cosRepositoryFile, []byte(expectedRepository+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &resticFakeRunner{repositoryNew: true, snapshots: `[{"id":"cos-snapshot"}]`}
	repository := NewResticRepository(Config{
		LocalRepositoryFile: localRepositoryFile,
		COSRepositoryFile:   cosRepositoryFile,
		ResticPasswordFile:  resticPasswordFile,
		COSEndpoint:         "https://cos.ap-guangzhou.myqcloud.com",
		COSRegion:           "ap-guangzhou",
		COSBucket:           "yunling-backup-1250000000",
		COSPrefix:           "yunling",
		COSSecretIDFile:     secretIDFile,
		COSSecretKeyFile:    secretKeyFile,
	}, runner)

	snapshotID, err := repository.CopyToCOS(context.Background(), "local-snapshot", runID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotID != "cos-snapshot" {
		t.Fatalf("COS 快照 ID 错误：%q", snapshotID)
	}
	stored, err := os.ReadFile(cosRepositoryFile)
	if err != nil || strings.TrimSpace(string(stored)) != expectedRepository {
		t.Fatalf("COS repository 文件错误：%q err=%v", stored, err)
	}
	joinedCalls := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		joined := strings.Join(call.args, " ")
		joinedCalls = append(joinedCalls, joined)
		for _, required := range []string{
			"-o s3.bucket-lookup=dns", "-o s3.region=ap-guangzhou",
			"--repository-file " + cosRepositoryFile,
			"--password-file " + resticPasswordFile,
		} {
			if !strings.Contains(joined, required) {
				t.Fatalf("COS 命令缺少 %q：%q", required, joined)
			}
		}
		if strings.Contains(joined, "cos-secret-id") || strings.Contains(joined, "cos-secret-key") {
			t.Fatalf("COS 凭据不得出现在命令行：%q", joined)
		}
		if call.env["AWS_ACCESS_KEY_ID"] != "cos-secret-id" || call.env["AWS_SECRET_ACCESS_KEY"] != "cos-secret-key" {
			t.Fatalf("COS 凭据必须只进入子进程环境：%+v", call.env)
		}
	}
	if !strings.Contains(strings.Join(joinedCalls, "\n"), "init --from-repository-file "+localRepositoryFile+" --from-password-file "+resticPasswordFile+" --copy-chunker-params") {
		t.Fatalf("远端初始化必须复制 chunker 参数：%v", joinedCalls)
	}
	if !strings.Contains(strings.Join(joinedCalls, "\n"), "copy --from-repository-file "+localRepositoryFile+" --from-password-file "+resticPasswordFile+" local-snapshot") {
		t.Fatalf("COS copy 参数错误：%v", joinedCalls)
	}
}

func TestResticForgetUsesSevenAndThirtyDayWindows(t *testing.T) {
	directory := t.TempDir()
	localRepositoryFile := filepath.Join(directory, "local-repository")
	cosRepositoryFile := filepath.Join(directory, "cos-repository")
	passwordFile := filepath.Join(directory, "restic-password")
	secretIDFile := filepath.Join(directory, "cos-secret-id")
	secretKeyFile := filepath.Join(directory, "cos-secret-key")
	for path, value := range map[string]string{
		localRepositoryFile: "/var/lib/yunling-ops/local-repo",
		cosRepositoryFile:   "s3:https://cos.ap-guangzhou.myqcloud.com/bucket/yunling",
		passwordFile:        "restic-password",
		secretIDFile:        "secret-id",
		secretKeyFile:       "secret-key",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &resticFakeRunner{}
	repository := NewResticRepository(Config{
		LocalRepositoryFile: localRepositoryFile,
		COSRepositoryFile:   cosRepositoryFile,
		ResticPasswordFile:  passwordFile,
		COSEndpoint:         "https://cos.ap-guangzhou.myqcloud.com",
		COSRegion:           "ap-guangzhou",
		COSBucket:           "bucket",
		COSPrefix:           "yunling",
		COSSecretIDFile:     secretIDFile,
		COSSecretKeyFile:    secretKeyFile,
	}, runner)
	if err := repository.ForgetLocal(context.Background(), "7d"); err != nil {
		t.Fatal(err)
	}
	if err := repository.ForgetCOS(context.Background(), "30d"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("forget 调用数量错误：%+v", runner.calls)
	}
	if local := strings.Join(runner.calls[0].args, " "); !strings.HasSuffix(local, "forget --prune --keep-within 7d") {
		t.Fatalf("本机保留参数错误：%q", local)
	}
	if remote := strings.Join(runner.calls[1].args, " "); !strings.HasSuffix(remote, "forget --prune --keep-within 30d") {
		t.Fatalf("COS 保留参数错误：%q", remote)
	}
}

func (f *resticFakeRunner) Run(_ context.Context, name string, args []string, environment map[string]string) (CommandResult, error) {
	environmentCopy := make(map[string]string, len(environment))
	for key, value := range environment {
		environmentCopy[key] = value
	}
	f.calls = append(f.calls, exportCommandCall{name: name, args: append([]string(nil), args...), env: environmentCopy})
	joined := strings.Join(args, " ")
	if strings.HasSuffix(joined, "cat config") && f.repositoryNew {
		return CommandResult{ExitCode: 10}, errors.New("repository missing")
	}
	if strings.Contains(joined, "snapshots --json") {
		return CommandResult{Stdout: f.snapshots, ExitCode: 0}, nil
	}
	return CommandResult{ExitCode: 0}, nil
}

func TestResticSnapshotLocalInitializesBacksUpFindsUniqueSnapshotAndChecks(t *testing.T) {
	runID := uuid.NewString()
	runner := &resticFakeRunner{repositoryNew: true, snapshots: `[{"id":"snapshot-one","tags":["backup-run=` + runID + `"]}]`}
	repository := NewResticRepository(Config{
		LocalRepositoryFile: "/run/config/local-repository",
		ResticPasswordFile:  "/run/secrets/restic-password",
	}, runner)
	snapshotID, err := repository.SnapshotLocal(context.Background(), "/var/lib/yunling-ops/staging/"+runID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotID != "snapshot-one" {
		t.Fatalf("快照 ID 错误：%q", snapshotID)
	}
	wantFragments := []string{
		"--repository-file /run/config/local-repository --password-file /run/secrets/restic-password cat config",
		"--repository-file /run/config/local-repository --password-file /run/secrets/restic-password init",
		"--repository-file /run/config/local-repository --password-file /run/secrets/restic-password backup /var/lib/yunling-ops/staging/" + runID + " --tag backup-run=" + runID,
		"--repository-file /run/config/local-repository --password-file /run/secrets/restic-password snapshots --json --tag backup-run=" + runID,
		"--repository-file /run/config/local-repository --password-file /run/secrets/restic-password check --read-data-subset=5%",
	}
	if len(runner.calls) != len(wantFragments) {
		t.Fatalf("Restic 调用数量错误：%+v", runner.calls)
	}
	for index, want := range wantFragments {
		got := strings.Join(runner.calls[index].args, " ")
		if got != want {
			t.Fatalf("第 %d 个 Restic 命令错误：got=%q want=%q", index+1, got, want)
		}
		if strings.Contains(got, "password-value") {
			t.Fatal("命令行不得出现密码")
		}
	}
}

func TestResticSnapshotLocalRejectsZeroOrMultipleMatchingSnapshots(t *testing.T) {
	for _, snapshots := range []string{`[]`, `[{"id":"one"},{"id":"two"}]`} {
		runner := &resticFakeRunner{snapshots: snapshots}
		repository := NewResticRepository(Config{
			LocalRepositoryFile: "/run/config/local-repository",
			ResticPasswordFile:  "/run/secrets/restic-password",
		}, runner)
		if _, err := repository.SnapshotLocal(context.Background(), "/staging", uuid.NewString()); err == nil {
			t.Fatalf("快照查询 %s 必须失败", snapshots)
		}
	}
}
