package executor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunSystemdSpecPersistsActualNonzeroExit(t *testing.T) {
	if os.Getenv("YUNLING_EXIT_HELPER") == "1" {
		os.Exit(7)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, systemdSpecFileName)
	body, err := json.Marshal(systemdRunSpec{Arguments: []string{os.Args[0], "-test.run=^TestRunSystemdSpecPersistsActualNonzeroExit$"}, WorkingDirectory: dir, Environment: map[string]string{"YUNLING_EXIT_HELPER": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	code, err := RunSystemdSpec(path)
	if code != 7 || err == nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
	actual, err := readSystemdExitCode(filepath.Join(dir, systemdResultFileName))
	if err != nil || actual != 7 {
		t.Fatalf("saved code=%d err=%v", actual, err)
	}
}

func TestSystemdProcessUsesWorkloadResult(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		control, want int
		wantErr       bool
	}{
		{"success", "0\n", 0, 0, false},
		{"script failure", "7\n", 1, 7, true},
		{"nonzero despite control success", "42\n", 0, 42, false},
		{"control failure", "0\n", 1, -1, true},
		{"missing", "", 0, -1, true},
		{"invalid", "garbage", 0, -1, true},
		{"signal or unknown", "-1\n", 0, -1, true},
		{"oversized", "0000000000000000000000", 0, -1, true},
		{"out of range", "256", 0, -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			resultPath := filepath.Join(dir, systemdResultFileName)
			if tc.body != "" {
				if err := os.WriteFile(resultPath, []byte(tc.body), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			var controlErr error
			if tc.control != 0 {
				controlErr = errors.New("systemctl failed")
			}
			p := &systemdProcess{Process: instantSystemdTestProcess{exitCode: tc.control, err: controlErr}, specPath: filepath.Join(dir, systemdSpecFileName)}
			code, err := p.Wait()
			if code != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("code=%d err=%v", code, err)
			}
			if _, err := os.Stat(resultPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("result not cleaned: %v", err)
			}
		})
	}
}
