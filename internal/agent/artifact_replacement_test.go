package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yunling.local/platform/internal/agentprotocol"
)

func TestArtifactCollectorRejectsFileReplacedAfterInventory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "run-1")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.csv", "two.csv", ".replacement"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("same length"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var uploads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		if uploads == 1 {
			other := "one.csv"
			if strings.HasSuffix(r.URL.Path, "/one.csv") {
				other = "two.csv"
			}
			if err := os.Remove(filepath.Join(directory, other)); err != nil {
				t.Error(err)
			}
			if err := os.Rename(filepath.Join(directory, ".replacement"), filepath.Join(directory, other)); err != nil {
				t.Error(err)
			}
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	collector, err := NewArtifactCollector(root, server.URL, "credential", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = collector.CollectAndUpload(context.Background(), agentprotocol.Assignment{RunID: "run-1", Artifacts: &agentprotocol.ArtifactPolicy{AllowedGlobs: []string{"*.csv"}, MaxFileBytes: 100, MaxTotalBytes: 200}})
	if err == nil || !strings.Contains(err.Error(), "被替换") || uploads != 1 {
		t.Fatalf("replacement accepted: uploads=%d err=%v", uploads, err)
	}
}
