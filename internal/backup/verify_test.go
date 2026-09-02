package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type verifierFakeRestorer struct {
	destination string
	err         error
}

func (r *verifierFakeRestorer) RestoreFromCOS(_ context.Context, _ string, destination string) error {
	r.destination = destination
	if err := os.MkdirAll(filepath.Join(destination, "database"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(destination, "objects", "scripts"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(destination, "metadata"), 0o700); err != nil {
		return err
	}
	for name, contents := range map[string]string{
		"database/yunling.dump":    "database-dump",
		"objects/scripts/one.zip":  "script-object",
		"metadata/deployment.json": `{"migrationVersion":"12"}`,
	} {
		if err := os.WriteFile(filepath.Join(destination, filepath.FromSlash(name)), []byte(contents), 0o600); err != nil {
			return err
		}
	}
	manifest, err := buildManifestAt(destination, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	if err != nil {
		return err
	}
	if _, err := WriteManifest(destination, manifest); err != nil {
		return err
	}
	return r.err
}

type verifierFakeRunner struct {
	calls     []exportCommandCall
	failStage string
	dropCount int
}

func (r *verifierFakeRunner) Run(_ context.Context, name string, args []string, environment map[string]string) (CommandResult, error) {
	r.calls = append(r.calls, exportCommandCall{name: name, args: append([]string(nil), args...), env: environment})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "DROP DATABASE") {
		r.dropCount++
		if r.failStage == "cleanup" {
			return CommandResult{ExitCode: 1}, errors.New("drop failed")
		}
		return CommandResult{ExitCode: 0}, nil
	}
	if strings.Contains(joined, "CREATE DATABASE") && r.failStage == "create" {
		return CommandResult{ExitCode: 1}, errors.New("create failed")
	}
	if name == "/usr/bin/pg_restore" && r.failStage == "restore" {
		return CommandResult{ExitCode: 1}, errors.New("pg_restore failed")
	}
	if strings.Contains(joined, "max(version)") {
		if r.failStage == "check" {
			return CommandResult{ExitCode: 1}, errors.New("check failed")
		}
		return CommandResult{Stdout: "12\n", ExitCode: 0}, nil
	}
	if strings.Contains(joined, "artifact_uri") {
		return CommandResult{Stdout: "scripts/one.zip\n", ExitCode: 0}, nil
	}
	return CommandResult{ExitCode: 0}, nil
}

func TestTemporaryDatabaseNameStrictlyRejectsProductionOrUnsafeNames(t *testing.T) {
	valid := "yunling_verify_" + strings.Repeat("a", 32)
	if err := ValidateTemporaryDatabase(valid, "yunling"); err != nil {
		t.Fatalf("合法临时数据库名被拒绝：%v", err)
	}
	for _, name := range []string{
		"yunling", "yunling_verify_short", "yunling-verify-" + strings.Repeat("a", 32),
		"yunling_verify_" + strings.Repeat("g", 32), `yunling_verify_"bad`, " yunling_verify_" + strings.Repeat("a", 32),
	} {
		if err := ValidateTemporaryDatabase(name, "yunling"); err == nil {
			t.Fatalf("必须拒绝临时数据库名 %q", name)
		}
	}
}

func TestVerifierRestoresChecksDatabaseAndAlwaysDropsTemporaryDatabase(t *testing.T) {
	root := t.TempDir()
	passwordFile := filepath.Join(root, "verify-password")
	if err := os.WriteFile(passwordFile, []byte("verify-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	restorer := &verifierFakeRestorer{}
	runner := &verifierFakeRunner{}
	verifier := NewVerifier(Config{
		BackupDatabaseURL:          "postgres://backup@postgres:5432/yunling?sslmode=disable",
		VerificationDatabaseURL:    "postgres://verifier@postgres:5432/postgres?sslmode=disable",
		VerifyDatabasePasswordFile: passwordFile,
		Root:                       root,
	}, restorer, runner, NewRunPaths(root))
	verification := RestoreVerification{ID: uuid.NewString()}
	backupRun := BackupRun{ID: uuid.NewString(), Status: StatusSucceeded, COSSnapshotID: "cos-snapshot"}

	result, err := verifier.Verify(context.Background(), verification, backupRun)
	if err != nil {
		t.Fatal(err)
	}
	if result.MigrationVersion != "12" || result.CheckedObjects != 1 || runner.dropCount != 1 {
		t.Fatalf("恢复校验结果错误：result=%+v drop=%d", result, runner.dropCount)
	}
	if err := ValidateTemporaryDatabase(result.TemporaryDatabase, "yunling"); err != nil {
		t.Fatalf("生成的临时数据库名不安全：%q %v", result.TemporaryDatabase, err)
	}
	if _, err := os.Stat(restorer.destination); !os.IsNotExist(err) {
		t.Fatalf("成功后恢复目录必须删除：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "staging", verification.ID)); !os.IsNotExist(err) {
		t.Fatalf("成功后同一校验的暂存目录必须删除：%v", err)
	}
	joined := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		joined = append(joined, call.name+" "+strings.Join(call.args, " "))
	}
	all := strings.Join(joined, "\n")
	for _, required := range []string{"CREATE DATABASE", "/usr/bin/pg_restore", "--exit-on-error", "--no-owner", "--no-privileges", "DROP DATABASE"} {
		if !strings.Contains(all, required) {
			t.Fatalf("恢复流程缺少 %q：%s", required, all)
		}
	}
}

func TestVerifierCleansDatabaseAndDirectoryAfterEveryFailureStage(t *testing.T) {
	for _, stage := range []string{"cos", "restore", "check"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			passwordFile := filepath.Join(root, "verify-password")
			if err := os.WriteFile(passwordFile, []byte("verify-password"), 0o600); err != nil {
				t.Fatal(err)
			}
			restorer := &verifierFakeRestorer{}
			if stage == "cos" {
				restorer.err = errors.New("cos restore failed")
			}
			runner := &verifierFakeRunner{failStage: stage}
			verifier := NewVerifier(Config{
				BackupDatabaseURL:          "postgres://backup@postgres:5432/yunling?sslmode=disable",
				VerificationDatabaseURL:    "postgres://verifier@postgres:5432/postgres?sslmode=disable",
				VerifyDatabasePasswordFile: passwordFile,
				Root:                       root,
			}, restorer, runner, NewRunPaths(root))
			verificationID := uuid.NewString()
			_, err := verifier.Verify(context.Background(), RestoreVerification{ID: verificationID}, BackupRun{
				ID: uuid.NewString(), Status: StatusSucceeded, COSSnapshotID: "cos",
			})
			if err == nil {
				t.Fatalf("阶段 %s 必须失败", stage)
			}
			if runner.dropCount != 1 {
				t.Fatalf("阶段 %s 失败后必须尝试删除临时数据库", stage)
			}
			if _, statErr := os.Stat(restorer.destination); !os.IsNotExist(statErr) {
				t.Fatalf("阶段 %s 失败后必须删除恢复目录：%v", stage, statErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, "staging", verificationID)); !os.IsNotExist(statErr) {
				t.Fatalf("阶段 %s 后同一校验暂存目录必须删除：%v", stage, statErr)
			}
		})
	}
}

func TestVerifierReportsPrimaryAndCleanupFailuresWithoutSensitiveOutput(t *testing.T) {
	root := t.TempDir()
	passwordFile := filepath.Join(root, "verify-password")
	if err := os.WriteFile(passwordFile, []byte("verify-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	restorer := &verifierFakeRestorer{err: errors.New("sensitive restore error")}
	runner := &verifierFakeRunner{failStage: "cleanup"}
	verifier := NewVerifier(Config{
		BackupDatabaseURL:          "postgres://backup@postgres:5432/yunling?sslmode=disable",
		VerificationDatabaseURL:    "postgres://verifier@postgres:5432/postgres?sslmode=disable",
		VerifyDatabasePasswordFile: passwordFile,
		Root:                       root,
	}, restorer, runner, NewRunPaths(root))
	result, err := verifier.Verify(context.Background(), RestoreVerification{ID: uuid.NewString()}, BackupRun{
		ID: uuid.NewString(), Status: StatusSucceeded, COSSnapshotID: "cos",
	})
	if err == nil || !strings.Contains(err.Error(), "恢复校验失败") || !strings.Contains(err.Error(), "清理失败") {
		t.Fatalf("组合错误不完整：%v", err)
	}
	if strings.Contains(result.ErrorMessage, "sensitive") || len(result.ErrorMessage) > 4096 {
		t.Fatalf("持久化错误必须安全且有界：%q", result.ErrorMessage)
	}
}
