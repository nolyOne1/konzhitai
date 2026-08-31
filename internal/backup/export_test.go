package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type exportCommandCall struct {
	name string
	args []string
	env  map[string]string
}

type exportFakeRunner struct{ calls []exportCommandCall }

func (f *exportFakeRunner) Run(_ context.Context, name string, args []string, environment map[string]string) (CommandResult, error) {
	environmentCopy := make(map[string]string, len(environment))
	for key, value := range environment {
		environmentCopy[key] = value
	}
	f.calls = append(f.calls, exportCommandCall{name: name, args: append([]string(nil), args...), env: environmentCopy})
	switch name {
	case "/usr/bin/pg_dump":
		for _, argument := range args {
			if strings.HasPrefix(argument, "--file=") {
				path := strings.TrimPrefix(argument, "--file=")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					return CommandResult{}, err
				}
				if err := os.WriteFile(path, []byte("database-dump"), 0o600); err != nil {
					return CommandResult{}, err
				}
			}
		}
	case "/usr/bin/mc":
		destination := args[len(args)-1]
		if err := os.MkdirAll(filepath.Join(destination, "scripts"), 0o700); err != nil {
			return CommandResult{}, err
		}
		if err := os.WriteFile(filepath.Join(destination, "scripts", "one.zip"), []byte("object-one"), 0o600); err != nil {
			return CommandResult{}, err
		}
	}
	return CommandResult{ExitCode: 0}, nil
}

func TestExporterDumpsDatabaseBeforeMirroringObjects(t *testing.T) {
	root := t.TempDir()
	configuration := exportTestConfig(t, root)
	runner := &exportFakeRunner{}
	exporter := NewExporter(configuration, runner, NewRunPaths(root), DeploymentMetadata{
		GitRevision: "revision-1", MigrationVersion: "12", ImageDigests: map[string]string{"api": "sha256:test"},
	}, func() time.Time { return time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC) })
	run := BackupRun{ID: uuid.NewString()}

	result, err := exporter.Export(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || runner.calls[0].name != "/usr/bin/pg_dump" || runner.calls[1].name != "/usr/bin/mc" {
		t.Fatalf("导出顺序错误：%+v", runner.calls)
	}
	dumpPath := filepath.Join(result.Root, "database", "yunling.dump")
	if strings.Join(runner.calls[0].args, " ") != "--format=custom --file="+dumpPath+" "+configuration.BackupDatabaseURL {
		t.Fatalf("pg_dump 参数错误：%v", runner.calls[0].args)
	}
	objectsPath := filepath.Join(result.Root, "objects")
	if strings.Join(runner.calls[1].args, " ") != "mirror --overwrite --remove local/yunling "+objectsPath {
		t.Fatalf("mc mirror 参数错误：%v", runner.calls[1].args)
	}
	if runner.calls[0].env["PGPASSWORD"] != "database-password" {
		t.Fatal("数据库密码必须只通过子进程环境提供")
	}
	if !strings.HasPrefix(runner.calls[1].env["MC_HOST_local"], "http://") || strings.Contains(strings.Join(runner.calls[1].args, " "), "minio-secret") {
		t.Fatal("MinIO 凭据必须只通过子进程环境提供")
	}
	if result.Manifest.Database.Path != "database/yunling.dump" || result.ObjectCount != 1 || result.ManifestSHA256 == "" {
		t.Fatalf("导出结果不完整：%+v", result)
	}
}

func exportTestConfig(t *testing.T, root string) Config {
	t.Helper()
	write := func(name, value string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return Config{
		BackupDatabaseURL:          "postgres://backup@postgres:5432/yunling?sslmode=disable",
		BackupDatabasePasswordFile: write("database-password", "database-password"),
		MinIOEndpoint:              "http://minio:9000",
		MinIOBucket:                "yunling",
		MinIOAccessKeyFile:         write("minio-access", "minio-access"),
		MinIOSecretKeyFile:         write("minio-secret", "minio-secret"),
		Root:                       root,
	}
}
