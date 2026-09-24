package logstream

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/task"
	"yunling.local/platform/internal/testpostgres"
)

func TestArchiveWorkerWaitsForTerminalQuietLogsAndRearchivesLateChunks(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000007_run_observability.up.sql")
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	testpostgres.ApplyMigration(t, db, "000019_log_archive_progress.up.sql")
	ctx := context.Background()
	now := time.Now().UTC()
	var runID task.RunID
	err := db.QueryRow(ctx, `
		WITH script AS (INSERT INTO scripts(name,runtime) VALUES ('归档测试','bash') RETURNING id),
		version AS (INSERT INTO script_versions(script_id,version,artifact_uri,artifact_sha256,entrypoint)
			SELECT id,1,'test.tar.gz',repeat('a',64),'main.sh' FROM script RETURNING id,script_id),
		definition AS (INSERT INTO task_definitions(name,script_id,required_runtime)
			SELECT '归档任务',script_id,'bash' FROM version RETURNING id)
		INSERT INTO task_runs(task_definition_id,script_version_id,trigger_type,state,execution_token,updated_at)
		SELECT definition.id,version.id,'manual','running','archive-token',$1 FROM definition,version RETURNING id
	`, now.Add(-time.Minute)).Scan(&runID)
	if err != nil {
		t.Fatal(err)
	}
	chunks := NewPostgresChunkStore(db)
	if err := chunks.Insert(ctx, LogChunk{RunID: string(runID), ExecutionToken: "archive-token", Stream: StreamStdout, Sequence: 1, Content: "首块日志\n", CreatedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresArchiveRepository(db)
	objects := &memoryObjectStore{items: map[string][]byte{}}
	clock := now.Add(time.Minute)
	worker := NewArchiveWorker(repository, NewArchiver(repository, objects, 1, func() time.Time { return clock }), func() time.Time { return clock })
	if err := worker.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := repository.ArchiveStatus(ctx, runID)
	if err != nil || info.Available {
		t.Fatalf("running task archived: %+v %v", info, err)
	}
	if _, err := db.Exec(ctx, `UPDATE task_runs SET state='succeeded',finished_at=$2 WHERE id=$1`, runID, now); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(5 * time.Second)
	if err := worker.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	info, err = repository.ArchiveStatus(ctx, runID)
	if err != nil || info.Available {
		t.Fatalf("fresh terminal logs archived: %+v %v", info, err)
	}
	clock = now.Add(time.Minute)
	if err := worker.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := repository.ArchiveStatus(ctx, runID)
	if err != nil || !first.Available || !first.Current || first.ChunkCount != 1 {
		t.Fatalf("initial archive: %+v %v", first, err)
	}
	if err := worker.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(objects.items) != 1 {
		t.Fatalf("unchanged logs uploaded again: %d", len(objects.items))
	}
	if _, err := db.Exec(ctx, `INSERT INTO log_chunks(task_run_id, execution_token, stream, sequence, content, byte_size, created_at, received_at)
		VALUES ($1, 'archive-token', 'stdout', 2, $2, octet_length($2::text), $3, $4)`, runID, "断线后迟到日志\n", now.Add(-2*time.Hour), clock); err != nil {
		t.Fatal(err)
	}
	info, err = repository.ArchiveStatus(ctx, runID)
	if err != nil || info.Current {
		t.Fatalf("late chunk not detected: %+v %v", info, err)
	}
	if err := worker.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(objects.items) != 1 {
		t.Fatal("late chunk quiet period ignored")
	}
	clock = clock.Add(time.Minute)
	if err := worker.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	latest, err := repository.ArchiveStatus(ctx, runID)
	if err != nil || !latest.Current || latest.ChunkCount != 2 || latest.ObjectKey == first.ObjectKey || len(objects.items) != 2 {
		t.Fatalf("late archive: %+v %v", latest, err)
	}
	// An older worker finishing late must never replace the fuller snapshot.
	if err := repository.SaveArchive(ctx, ArchiveRecord{RunID: runID, ObjectKey: first.ObjectKey, ByteSize: first.ByteSize, SHA256: first.SHA256, FirstLogAt: now, LastLogAt: now, ArchivedAt: clock, ChunkCount: 1, LastLogCursor: 1}); err != nil {
		t.Fatal(err)
	}
	info, err = repository.ArchiveStatus(ctx, runID)
	if err != nil || info.ObjectKey != latest.ObjectKey || !info.Current {
		t.Fatalf("archive regressed: %+v %v", info, err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM log_chunks WHERE task_run_id=$1`, runID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("database logs removed: %d %v", count, err)
	}
}
