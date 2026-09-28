//go:build linux

package testpostgres

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// Exercise the public fixture, not a fake PostgreSQL executable. Both real
// databases stay alive together; disposing one must not affect the other.
func TestSharedBinariesKeepFixtureDatabasesIsolated(t *testing.T) {
	first := &cleanupScope{TB: t}
	second := &cleanupScope{TB: t}
	t.Cleanup(first.close)
	t.Cleanup(second.close)
	start := time.Now()
	a := Start(first)
	firstDuration := time.Since(start)
	start = time.Now()
	b := Start(second)
	t.Logf("fixture startup first=%s second=%s (observations, not a speed threshold)", firstDuration, time.Since(start))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var firstPID, secondPID, firstPort, secondPort int
	var firstData, secondData string
	query := "SELECT pg_backend_pid(), inet_server_port(), current_setting('data_directory')"
	if err := a.QueryRow(ctx, query).Scan(&firstPID, &firstPort, &firstData); err != nil {
		t.Fatal(err)
	}
	if err := b.QueryRow(ctx, query).Scan(&secondPID, &secondPort, &secondData); err != nil {
		t.Fatal(err)
	}
	if firstPID == secondPID || firstPort == secondPort || firstData == secondData {
		t.Fatalf("fixtures share process, port or data: %d/%d %d/%d %q/%q", firstPID, secondPID, firstPort, secondPort, firstData, secondData)
	}
	if _, err := a.Exec(ctx, "CREATE TABLE fixture_isolation_probe (id int)"); err != nil {
		t.Fatal(err)
	}
	var leaked *string
	if err := b.QueryRow(ctx, "SELECT to_regclass('fixture_isolation_probe')::text").Scan(&leaked); err != nil || leaked != nil {
		t.Fatalf("database data leaked across fixtures: table=%v err=%v", leaked, err)
	}
	first.close()
	if err := b.Ping(ctx); err != nil {
		t.Fatalf("cleaning first fixture stopped the second: %v", err)
	}
	second.close()
	for _, data := range []string{firstData, secondData} {
		if _, err := os.Stat(data); !os.IsNotExist(err) {
			t.Fatalf("private data not removed: %s: %v", data, err)
		}
		runtimePath := filepath.Join(filepath.Dir(filepath.Dir(data)), "runtime", filepath.Base(data))
		if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
			t.Fatalf("private runtime not removed: %s: %v", runtimePath, err)
		}
	}

	key, err := binaryCacheKey()
	if err != nil {
		t.Fatal(err)
	}
	binaries := filepath.Join(RepositoryRoot(t), ".tools", "embedded-postgres", "binaries", key)
	if err := validateBinaryDirectory(binaries, key); err != nil {
		t.Fatalf("fixture cleanup removed shared binaries: %v", err)
	}

	// A warm cache must work even with no archive and an unreachable download
	// source. If the dependency tries XZ extraction again, this Start fails.
	work := t.TempDir()
	config := embeddedpostgres.DefaultConfig().Version(embeddedpostgres.V18).
		BinaryRepositoryURL("http://127.0.0.1:1").CachePath(filepath.Join(work, "empty-archive-cache")).
		BinariesPath(binaries).RuntimePath(filepath.Join(work, "runtime")).DataPath(filepath.Join(work, "data")).
		Port(availablePort(t)).Database("isolated_warm_cache").Username("postgres").Password("postgres").
		Locale("C").Encoding("UTF8").StartTimeout(45 * time.Second).Logger(io.Discard)
	database := embeddedpostgres.NewDatabase(config)
	if err := database.Start(); err != nil {
		_ = database.Stop()
		t.Fatalf("warm binaries unexpectedly required archive/download: %v", err)
	}
	if err := database.Stop(); err != nil {
		t.Fatal(err)
	}
}

type cleanupScope struct {
	testing.TB
	callbacks []func()
}

func (scope *cleanupScope) Cleanup(callback func()) {
	scope.callbacks = append(scope.callbacks, callback)
}
func (scope *cleanupScope) close() {
	for len(scope.callbacks) > 0 {
		last := len(scope.callbacks) - 1
		callback := scope.callbacks[last]
		scope.callbacks = scope.callbacks[:last]
		callback()
	}
}
