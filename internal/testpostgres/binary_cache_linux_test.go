//go:build linux

package testpostgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const binaryCacheChildEnv = "YUNLING_TEST_BINARY_CACHE_CHILD"
const binaryCacheProtocolKey = "protocol-v1-linux-test"
const binaryCacheInjectedFailure = "injected binary preparation failure"

// These tests exercise the cache protocol with ordinary executable files. They
// never execute those files or claim to verify a real PostgreSQL installation.
func TestBinaryDirectoryAcrossProcessesPreparesOnce(t *testing.T) {
	root, control := binaryCacheTestDirectories(t)
	first := startBinaryCacheProcess(t, root, control, "first", "hold")
	waitForBinaryCacheSignal(t, first, filepath.Join(control, "blocked-first"))
	assertNoBinaryCacheReady(t, root)

	second := startBinaryCacheProcess(t, root, control, "second", "success")
	waitForBinaryCacheSignal(t, second, filepath.Join(control, "entered-second"))
	// The first callback is deliberately incomplete. The second process must
	// neither call prepare nor return a directory while that lock is held.
	select {
	case <-second.done:
		t.Fatalf("second process returned before preparation completed: %v\n%s", second.err, second.output.String())
	case <-time.After(250 * time.Millisecond):
	}
	assertBinaryCachePreparationCount(t, control, 1)
	assertNoBinaryCacheReady(t, root)
	if err := os.WriteFile(filepath.Join(control, "release"), []byte("continue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first.waitSuccess(t)
	second.waitSuccess(t)

	firstResult := readBinaryCacheResult(t, control, "first")
	secondResult := readBinaryCacheResult(t, control, "second")
	if firstResult.Path != secondResult.Path {
		t.Fatalf("processes received different cache directories: %q and %q", firstResult.Path, secondResult.Path)
	}
	assertPublishedBinaryCache(t, firstResult.Path)
	assertBinaryCachePreparationCount(t, control, 1)
	stage, err := os.ReadFile(filepath.Join(control, "prepare-first"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stage) == firstResult.Path {
		t.Fatal("prepare was given the published directory instead of an isolated staging directory")
	}
	if _, err := os.Stat(string(stage)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory was not atomically moved away: %v", err)
	}
}

func TestBinaryDirectoryFailedPreparationCanRetry(t *testing.T) {
	root, control := binaryCacheTestDirectories(t)
	failed := startBinaryCacheProcess(t, root, control, "failed", "fail")
	failed.waitSuccess(t) // The child expects the injected callback error.
	if result := readBinaryCacheResult(t, control, "failed"); !strings.Contains(result.Error, binaryCacheInjectedFailure) {
		t.Fatalf("preparation failure was not returned: %+v", result)
	}
	assertNoBinaryCacheReady(t, root)

	retried := startBinaryCacheProcess(t, root, control, "retried", "success")
	retried.waitSuccess(t)
	assertPublishedBinaryCache(t, readBinaryCacheResult(t, control, "retried").Path)
	assertBinaryCachePreparationCount(t, control, 2)
}

func TestBinaryDirectoryKilledInitializerCanRecover(t *testing.T) {
	root, control := binaryCacheTestDirectories(t)
	interrupted := startBinaryCacheProcess(t, root, control, "interrupted", "hold")
	waitForBinaryCacheSignal(t, interrupted, filepath.Join(control, "blocked-interrupted"))
	assertNoBinaryCacheReady(t, root)
	recovered := startBinaryCacheProcess(t, root, control, "recovered", "success")
	waitForBinaryCacheSignal(t, recovered, filepath.Join(control, "entered-recovered"))
	select {
	case <-recovered.done:
		t.Fatalf("waiting process returned while the initializer held the lock: %v\n%s", recovered.err, recovered.output.String())
	case <-time.After(250 * time.Millisecond):
	}
	assertBinaryCachePreparationCount(t, control, 1)
	assertNoBinaryCacheReady(t, root)
	if err := interrupted.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	interrupted.wait(t)
	if interrupted.err == nil {
		t.Fatal("initializer unexpectedly exited successfully after being killed")
	}
	recovered.waitSuccess(t)
	assertPublishedBinaryCache(t, readBinaryCacheResult(t, control, "recovered").Path)
	assertBinaryCachePreparationCount(t, control, 2)
}

// Only the subprocess runs this body. Running the package normally does not
// create a fixture for this helper entry point.
func TestBinaryDirectoryCacheProcess(t *testing.T) {
	if os.Getenv(binaryCacheChildEnv) != "1" {
		return
	}
	root := os.Getenv("YUNLING_TEST_BINARY_CACHE_ROOT")
	control := os.Getenv("YUNLING_TEST_BINARY_CACHE_CONTROL")
	id := os.Getenv("YUNLING_TEST_BINARY_CACHE_ID")
	mode := os.Getenv("YUNLING_TEST_BINARY_CACHE_MODE")
	if root == "" || control == "" || id == "" || (mode != "hold" && mode != "fail" && mode != "success") {
		t.Fatal("incomplete binary cache subprocess configuration")
	}
	if err := os.WriteFile(filepath.Join(control, "entered-"+id), []byte("entered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := ensureBinaryDirectory(root, binaryCacheProtocolKey, func(destination string) error {
		// O_EXCL also detects an unexpected second callback in this process.
		marker, err := os.OpenFile(filepath.Join(control, "prepare-"+id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := marker.WriteString(destination)
		closeErr := marker.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return err
		}
		if err := writeBinaryCacheFixture(destination, "pg_ctl"); err != nil {
			return err
		}
		if mode == "fail" {
			return errors.New(binaryCacheInjectedFailure)
		}
		if mode == "hold" {
			if err := os.WriteFile(filepath.Join(control, "blocked-"+id), []byte("partial\n"), 0o600); err != nil {
				return err
			}
			deadline := time.Now().Add(15 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(control, "release")); err == nil {
					break
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if time.Now().After(deadline) {
					return errors.New("timed out waiting to release cache preparation")
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		for _, name := range []string{"initdb", "postgres"} {
			if err := writeBinaryCacheFixture(destination, name); err != nil {
				return err
			}
		}
		return nil
	})
	result := binaryCacheProcessResult{Path: path}
	if err != nil {
		result.Error = err.Error()
	}
	if mode == "fail" {
		if err == nil || !strings.Contains(err.Error(), binaryCacheInjectedFailure) {
			t.Fatalf("expected injected preparation error, got %v", err)
		}
	} else {
		if err != nil {
			t.Fatalf("ensure binary directory: %v", err)
		}
		assertPublishedBinaryCache(t, path)
	}
	contents, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "result-"+id+".json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

type binaryCacheProcessResult struct {
	Path  string
	Error string
}

type binaryCacheProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	output bytes.Buffer
}

func binaryCacheTestDirectories(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	control := filepath.Join(base, "control")
	if err := os.Mkdir(control, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "cache"), control
}

func startBinaryCacheProcess(t *testing.T, root, control, id, mode string) *binaryCacheProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	child := &binaryCacheProcess{done: make(chan struct{})}
	child.cmd = exec.CommandContext(ctx, executable, "-test.run=^TestBinaryDirectoryCacheProcess$", "-test.count=1", "-test.timeout=20s")
	child.cmd.Env = append(os.Environ(),
		binaryCacheChildEnv+"=1",
		"YUNLING_TEST_BINARY_CACHE_ROOT="+root,
		"YUNLING_TEST_BINARY_CACHE_CONTROL="+control,
		"YUNLING_TEST_BINARY_CACHE_ID="+id,
		"YUNLING_TEST_BINARY_CACHE_MODE="+mode,
	)
	child.cmd.Stdout = &child.output
	child.cmd.Stderr = &child.output
	child.cmd.WaitDelay = time.Second
	if err := child.cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() {
		child.err = child.cmd.Wait()
		close(child.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-child.done:
		case <-time.After(5 * time.Second):
			t.Error("binary cache subprocess did not exit after cancellation")
		}
	})
	return child
}

func (child *binaryCacheProcess) wait(t *testing.T) {
	t.Helper()
	select {
	case <-child.done:
	case <-time.After(25 * time.Second):
		t.Fatal("timed out waiting for binary cache subprocess")
	}
}

func (child *binaryCacheProcess) waitSuccess(t *testing.T) {
	t.Helper()
	child.wait(t)
	if child.err != nil {
		t.Fatalf("binary cache subprocess failed: %v\n%s", child.err, child.output.String())
	}
}

func waitForBinaryCacheSignal(t *testing.T, child *binaryCacheProcess, path string) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-child.done:
			t.Fatalf("subprocess exited before signal %s: %v\n%s", filepath.Base(path), child.err, child.output.String())
		case <-timer.C:
			t.Fatalf("timed out waiting for signal %s", filepath.Base(path))
		case <-ticker.C:
		}
	}
}

func writeBinaryCacheFixture(destination, name string) error {
	if err := os.MkdirAll(filepath.Join(destination, "bin"), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(destination, "bin", name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(file, "cache protocol fixture: %s\n", name)
	return errors.Join(writeErr, file.Close())
}

func assertPublishedBinaryCache(t *testing.T, path string) {
	t.Helper()
	if path == "" {
		t.Fatal("empty published cache path")
	}
	ready, err := os.ReadFile(filepath.Join(path, ".ready"))
	if err != nil || string(ready) != binaryCacheProtocolKey+"\n" {
		t.Fatalf("invalid cache readiness marker: %q, %v", ready, err)
	}
	for _, name := range []string{"pg_ctl", "initdb", "postgres"} {
		file := filepath.Join(path, "bin", name)
		info, err := os.Lstat(file)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("binary %s is not a regular executable file: %s", name, info.Mode())
		}
		content, err := os.ReadFile(file)
		if err != nil || string(content) != fmt.Sprintf("cache protocol fixture: %s\n", name) {
			t.Fatalf("binary %s was incompletely published: %q, %v", name, content, err)
		}
	}
}

func assertNoBinaryCacheReady(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".ready" {
			return fmt.Errorf("incomplete preparation published readiness marker %s", path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func assertBinaryCachePreparationCount(t *testing.T, control string, want int) {
	t.Helper()
	entries, err := os.ReadDir(control)
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "prepare-") {
			got++
		}
	}
	if got != want {
		t.Fatalf("prepare callback count = %d, want %d", got, want)
	}
}

func readBinaryCacheResult(t *testing.T, control, id string) binaryCacheProcessResult {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(control, "result-"+id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var result binaryCacheProcessResult
	if err := json.Unmarshal(contents, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
