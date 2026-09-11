package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRunSystemdSpecSignalIsNotSuccessfulExit(t *testing.T) {
	if os.Getenv("YUNLING_SIGNAL_HELPER") == "1" {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		os.Exit(99)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, systemdSpecFileName)
	body, err := json.Marshal(systemdRunSpec{Arguments: []string{os.Args[0], "-test.run=^TestRunSystemdSpecSignalIsNotSuccessfulExit$"}, WorkingDirectory: dir, Environment: map[string]string{"YUNLING_SIGNAL_HELPER": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	code, err := RunSystemdSpec(path)
	if code != -1 || err == nil {
		t.Fatalf("signal result: code=%d err=%v", code, err)
	}
	code, err = readSystemdExitCode(filepath.Join(dir, systemdResultFileName))
	if code != -1 || err == nil {
		t.Fatalf("persisted signal result: code=%d err=%v", code, err)
	}
	info, err := os.Stat(filepath.Join(dir, systemdResultFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("agent group cannot read result: %v", info.Mode())
	}
}
