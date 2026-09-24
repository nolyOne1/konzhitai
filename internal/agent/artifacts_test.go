package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/executor"
)

func TestArtifactCollectorUploadsAllowedFilesAndSkipsPrivateMetadata(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run-1")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"report.csv": "结果,成功\n", ".env": "secret", "systemd-run-spec.json": "parameters and secrets", "stdout.log": "raw logs"} {
		if err := os.WriteFile(filepath.Join(runDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		sum := sha256.Sum256(body)
		if r.URL.Path != "/api/agent/runs/run-1/artifacts/report.csv" || r.Header.Get("Authorization") != "Bearer agent-credential" || r.Header.Get("X-Execution-Token") != "execution-token" || r.Header.Get("X-Content-SHA256") != hex.EncodeToString(sum[:]) || string(body) != "结果,成功\n" {
			t.Errorf("invalid authenticated upload: path=%s body=%q", r.URL.Path, body)
		}
		if requests == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	collector, err := NewArtifactCollector(root, server.URL, "agent-credential", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = collector.CollectAndUpload(context.Background(), agentprotocol.Assignment{RunID: "run-1", ExecutionToken: "execution-token", Artifacts: &agentprotocol.ArtifactPolicy{AllowedGlobs: []string{"*"}, MaxFileBytes: 1024, MaxTotalBytes: 2048}})
	if err != nil || requests != 2 {
		t.Fatalf("uploads=%d error=%v", requests, err)
	}
}

func TestArtifactCollectorRejectsLimitsAndEscapingDirectoriesBeforeUpload(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run-1")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.csv", "two.csv"} {
		if err := os.WriteFile(filepath.Join(runDir, name), []byte("123456"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var uploads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { uploads++; w.WriteHeader(http.StatusCreated) }))
	defer server.Close()
	collector, err := NewArtifactCollector(root, server.URL, "credential", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, assignment := range []agentprotocol.Assignment{
		{RunID: "run-1", Artifacts: &agentprotocol.ArtifactPolicy{AllowedGlobs: []string{"*.csv"}, MaxFileBytes: 4, MaxTotalBytes: 20}},
		{RunID: "run-1", Artifacts: &agentprotocol.ArtifactPolicy{AllowedGlobs: []string{"*.csv"}, MaxFileBytes: 10, MaxTotalBytes: 10}},
		{RunID: "../outside", Artifacts: &agentprotocol.ArtifactPolicy{AllowedGlobs: []string{"*.csv"}, MaxFileBytes: 10, MaxTotalBytes: 20}},
	} {
		if err := collector.CollectAndUpload(context.Background(), assignment); err == nil {
			t.Fatalf("unsafe policy accepted: %+v", assignment)
		}
	}
	if uploads != 0 {
		t.Fatalf("invalid collection uploaded %d files", uploads)
	}
	if err := collector.CollectAndUpload(context.Background(), agentprotocol.Assignment{RunID: "missing-directory"}); err != nil {
		t.Fatalf("disabled collection should do nothing: %v", err)
	}
}

func TestArtifactCollectorRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run-1")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.csv")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runDir, "report.csv")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	collector, err := NewArtifactCollector(root, "https://control.example", "credential", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := collector.CollectAndUpload(context.Background(), agentprotocol.Assignment{RunID: "run-1", Artifacts: &agentprotocol.ArtifactPolicy{AllowedGlobs: []string{"*.csv"}, MaxFileBytes: 10, MaxTotalBytes: 20}}); err == nil || !strings.Contains(err.Error(), "普通文件") {
		t.Fatalf("symlink accepted: %v", err)
	}
}

type artifactCollectorStub struct{ collect func() error }

func (s artifactCollectorStub) CollectAndUpload(context.Context, agentprotocol.Assignment) error {
	return s.collect()
}

type artifactOutput struct{ bytes.Buffer }

func (o *artifactOutput) OutputWriter(_, _, _ string) io.Writer { return &o.Buffer }

func TestArtifactCollectionFollowsTerminalReportAndPersistsFailureAsLog(t *testing.T) {
	transport := &fakeExecutionTransport{events: make(chan agentprotocol.RunEvent, 2)}
	output := &artifactOutput{}
	collector := artifactCollectorStub{collect: func() error {
		if len(transport.events) != 2 {
			t.Fatal("collection delayed terminal report")
		}
		return errors.New("文件过大")
	}}
	client := NewExecutionClient(nil, transport, WithRunArtifacts(collector, output))
	events := make(chan executor.Event, 2)
	events <- executor.Event{Sequence: 1, Type: executor.EventStarted}
	events <- executor.Event{Sequence: 2, Type: executor.EventSucceeded, Message: "执行成功"}
	close(events)
	client.forwardEvents(context.Background(), agentprotocol.Assignment{RunID: "run-1", Artifacts: &agentprotocol.ArtifactPolicy{}}, events, make(chan error, 1))
	<-transport.events
	terminal := <-transport.events
	if terminal.Type != "succeeded" || terminal.Message != "执行成功" || !strings.Contains(output.String(), "产物采集未完成：文件过大") {
		t.Fatalf("terminal=%+v log=%s", terminal, output.String())
	}
}
