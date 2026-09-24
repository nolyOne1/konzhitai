package runartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/artifact"
	"yunling.local/platform/internal/testpostgres"
)

type memoryObjects struct {
	mu     sync.Mutex
	values map[string]string
	fail   bool
}

func (m *memoryObjects) Put(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("storage unavailable")
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if previous, ok := m.values[key]; ok && previous != string(data) {
		return artifact.ErrObjectConflict
	}
	m.values[key] = string(data)
	return nil
}
func (m *memoryObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.values[key]
	if !ok {
		return nil, artifact.ErrObjectMissing
	}
	return io.NopCloser(strings.NewReader(data)), nil
}

func artifactDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000007_run_observability.up.sql")
	return db
}
func seedRun(t *testing.T, db *pgxpool.Pool, manifest string) UploadInput {
	t.Helper()
	ctx := context.Background()
	serverID := uuid.NewString()
	runID := uuid.NewString()
	token := uuid.NewString()
	_, err := db.Exec(ctx, `INSERT INTO servers(id,name) VALUES($1::uuid,$1::text)`, serverID)
	if err != nil {
		t.Fatal(err)
	}
	err = db.QueryRow(ctx, `WITH script AS (INSERT INTO scripts(name,runtime) VALUES($1,'bash') RETURNING id),
		version AS (INSERT INTO script_versions(script_id,version,artifact_uri,artifact_sha256,entrypoint,manifest)
			SELECT id,1,'bundle.tar.gz',repeat('a',64),'main.sh',$2::jsonb FROM script RETURNING id,script_id),
		definition AS (INSERT INTO task_definitions(name,script_id,required_runtime) SELECT $1,script_id,'bash' FROM version RETURNING id)
		INSERT INTO task_runs(id,task_definition_id,script_version_id,assigned_server_id,trigger_type,state,execution_token)
		SELECT $3,definition.id,version.id,$4,'manual','running',$5 FROM definition,version RETURNING id`, runID, manifest, runID, serverID, token).Scan(&runID)
	if err != nil {
		t.Fatal(err)
	}
	return UploadInput{RunID: runID, ServerID: serverID, ExecutionToken: token, Name: "report.csv"}
}
func bodyInput(base UploadInput, contents string) UploadInput {
	sum := sha256.Sum256([]byte(contents))
	base.ByteSize = int64(len(contents))
	base.SHA256 = hex.EncodeToString(sum[:])
	return base
}

func TestPostgresArtifactPolicyOwnershipIdempotencyAndDownload(t *testing.T) {
	db := artifactDatabase(t)
	objects := &memoryObjects{values: map[string]string{}}
	service := NewService(db, objects)
	ctx := context.Background()
	base := seedRun(t, db, `{"artifacts":{"allowedGlobs":["*.csv"],"maxFileBytes":8,"maxTotalBytes":12}}`)
	input := bodyInput(base, "report")
	for _, tc := range []struct {
		name   string
		mutate func(*UploadInput)
		body   string
		want   error
	}{
		{"wrong server", func(i *UploadInput) { i.ServerID = uuid.NewString() }, "report", ErrAccess},
		{"wrong execution", func(i *UploadInput) { i.ExecutionToken = "stale" }, "report", ErrAccess},
		{"path traversal", func(i *UploadInput) { i.Name = "../report.csv" }, "report", ErrInvalid},
		{"hidden file", func(i *UploadInput) { i.Name = ".secret.csv" }, "report", ErrInvalid},
		{"not allowed", func(i *UploadInput) { i.Name = "report.txt" }, "report", ErrInvalid},
		{"excess file size", func(i *UploadInput) { i.ByteSize = 9 }, "report123", ErrInvalid},
		{"wrong checksum", func(i *UploadInput) { i.SHA256 = strings.Repeat("a", 64) }, "report", ErrInvalid},
		{"short body", func(i *UploadInput) {}, "rep", ErrInvalid},
		{"extra body", func(i *UploadInput) {}, "reportx", ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := input
			tc.mutate(&invalid)
			_, _, err := service.Upload(ctx, invalid, strings.NewReader(tc.body))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
	if len(objects.values) != 0 {
		t.Fatal("rejected upload stored an object")
	}
	type result struct {
		record  Record
		created bool
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			record, created, err := service.Upload(ctx, input, strings.NewReader("report"))
			results <- result{record, created, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.record.ID != second.record.ID || first.created == second.created || len(objects.values) != 1 {
		t.Fatalf("concurrent results=%+v %+v objects=%d", first, second, len(objects.values))
	}
	changed := bodyInput(base, "other")
	if _, _, err := service.Upload(ctx, changed, strings.NewReader("other")); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed content=%v", err)
	}
	list, err := service.List(ctx, base.RunID)
	if err != nil || len(list) != 1 || list[0].ByteSize != 6 {
		t.Fatalf("list=%+v %v", list, err)
	}
	record, reader, err := service.Open(ctx, base.RunID, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(reader)
	reader.Close()
	if string(data) != "report" || record.SHA256 != input.SHA256 || !strings.HasSuffix(record.ObjectKey, input.SHA256) {
		t.Fatalf("download=%s %+v", data, record)
	}
	otherRun := seedRun(t, db, `{}`)
	if _, _, err := service.Open(ctx, otherRun.RunID, list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-run download=%v", err)
	}
	if _, _, err := service.Upload(ctx, bodyInput(otherRun, "report"), strings.NewReader("report")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unconfigured policy=%v", err)
	}
	// Runtime policy must use the immutable published version, never a mutable draft.
	if _, err := db.Exec(ctx, `INSERT INTO script_drafts(script_id,manifest) SELECT script_id,'{"artifacts":{"allowedGlobs":["*"],"maxFileBytes":100,"maxTotalBytes":100}}' FROM script_versions WHERE id=(SELECT script_version_id FROM task_runs WHERE id=$1)`, base.RunID); err != nil {
		t.Fatal(err)
	}
	invalid := bodyInput(base, "big-report")
	if _, _, err := service.Upload(ctx, invalid, strings.NewReader("big-report")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("draft policy used: %v", err)
	}
}

func TestPostgresArtifactQuotaIsAtomicAndStorageFailureDoesNotConsumeIt(t *testing.T) {
	db := artifactDatabase(t)
	objects := &memoryObjects{values: map[string]string{}}
	service := NewService(db, objects)
	ctx := context.Background()
	base := seedRun(t, db, `{"artifacts":{"allowedGlobs":["*.csv"],"maxFileBytes":8,"maxTotalBytes":8}}`)
	objects.fail = true
	if _, _, err := service.Upload(ctx, bodyInput(base, "data"), strings.NewReader("data")); err == nil {
		t.Fatal("storage failure ignored")
	}
	list, err := service.List(ctx, base.RunID)
	if err != nil || len(list) != 0 {
		t.Fatalf("failed upload indexed: %+v %v", list, err)
	}
	objects.fail = false
	results := make(chan error, 2)
	for _, name := range []string{"a.csv", "b.csv"} {
		input := bodyInput(base, "12345")
		input.Name = name
		go func() { _, _, err := service.Upload(ctx, input, strings.NewReader("12345")); results <- err }()
	}
	a, b := <-results, <-results
	if !((a == nil && errors.Is(b, ErrLimit)) || (b == nil && errors.Is(a, ErrLimit))) {
		t.Fatalf("quota race=%v %v", a, b)
	}
	list, err = service.List(ctx, base.RunID)
	if err != nil || len(list) != 1 || len(objects.values) != 1 {
		t.Fatalf("quota result=%+v %v", list, err)
	}
	// Even empty files count toward the per-run file limit.
	countBase := seedRun(t, db, `{"artifacts":{"allowedGlobs":["*.csv"],"maxFileBytes":8,"maxTotalBytes":8}}`)
	if _, err := db.Exec(ctx, `INSERT INTO run_artifacts(task_run_id,name,object_key,byte_size,sha256) SELECT $1::uuid,'file-'||n||'.csv',$1::text||'/'||n,0,repeat('a',64) FROM generate_series(1,100) AS n`, countBase.RunID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Upload(ctx, bodyInput(countBase, ""), strings.NewReader("")); !errors.Is(err, ErrLimit) {
		t.Fatalf("file count=%v", err)
	}
}
