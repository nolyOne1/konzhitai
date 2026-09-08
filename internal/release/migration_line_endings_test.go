package release

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHistoricalMigrationAllowsOnlyWholeTreeCRLFEquivalent(t *testing.T) {
	old := t.TempDir()
	newTree := t.TempDir()
	name := "000001_initial.up.sql"
	if err := os.WriteFile(filepath.Join(old, name), []byte("SELECT 1;\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	want, err := migrationTreeDigestThrough(old, 12)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		match      bool
	}{
		{"CRLF", "SELECT 1;\r\n", true},
		{"LF", "SELECT 1;\n", true},
		{"SQL changed", "SELECT 2;\n", false},
		{"space changed", "SELECT  1;\n", false},
		{"missing newline", "SELECT 1;", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(newTree, name), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := historicalMigrationDigestMatches(newTree, want)
			if err != nil || got != tc.match {
				t.Fatalf("match=%v err=%v want=%v", got, err, tc.match)
			}
		})
	}
}
