package logstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"yunling.local/platform/internal/artifact"
	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/task"
)

var (
	ErrArchiveNotFound = errors.New("此运行暂无压缩日志归档，请下载完整日志")
	ErrArchiveOutdated = errors.New("归档正在补齐迟到日志，请稍后重试或下载完整日志")
)

type ArchiveInfo struct {
	Available  bool       `json:"available"`
	Current    bool       `json:"current"`
	ByteSize   int64      `json:"byteSize,omitempty"`
	SHA256     string     `json:"sha256,omitempty"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	ChunkCount int64      `json:"chunkCount,omitempty"`
	ObjectKey  string     `json:"-"`
}

type ArchiveReader interface {
	ArchiveStatus(context.Context, task.RunID) (ArchiveInfo, error)
}

func (r *PostgresArchiveRepository) ArchiveStatus(ctx context.Context, runID task.RunID) (ArchiveInfo, error) {
	var info ArchiveInfo
	err := r.db.QueryRow(ctx, `
		SELECT archive.task_run_id IS NOT NULL,
			COALESCE(archive.last_log_cursor=logs.cursor AND archive.chunk_count=logs.chunks,false),
			COALESCE(archive.byte_size,0), COALESCE(archive.sha256,''), archive.archived_at,
			COALESCE(archive.chunk_count,0), COALESCE(archive.object_key,'')
		FROM task_runs AS run
		LEFT JOIN run_log_archives AS archive ON archive.task_run_id=run.id
		JOIN LATERAL (
			SELECT COALESCE(MAX(archive_cursor),0) AS cursor, COUNT(*) AS chunks FROM log_chunks WHERE task_run_id=run.id
		) AS logs ON true
		WHERE run.id=$1
	`, runID).Scan(&info.Available, &info.Current, &info.ByteSize, &info.SHA256, &info.ArchivedAt, &info.ChunkCount, &info.ObjectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return ArchiveInfo{}, task.ErrRunNotFound
	}
	return info, err
}

func ArchiveHandler(repository ArchiveReader, objects artifact.Store) http.Handler {
	router := http.NewServeMux()
	router.Handle("GET /api/runs/{id}/logs/archive/info", auth.Require(auth.PermissionRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info, err := archiveInfo(r, repository)
		if writeArchiveError(w, err) {
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(info)
	})))
	router.Handle("GET /api/runs/{id}/logs/archive", auth.Require(auth.PermissionRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info, err := archiveInfo(r, repository)
		if err == nil && !info.Available {
			err = ErrArchiveNotFound
		}
		if err == nil && !info.Current {
			err = ErrArchiveOutdated
		}
		if writeArchiveError(w, err) {
			return
		}
		body, err := objects.Open(r.Context(), info.ObjectKey)
		if writeArchiveError(w, err) {
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="run-logs.ndjson.gz"`)
		w.Header().Set("Content-Length", strconv.FormatInt(info.ByteSize, 10))
		w.Header().Set("ETag", `"`+info.SHA256+`"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.Copy(w, body)
	})))
	return router
}

func archiveInfo(r *http.Request, repository ArchiveReader) (ArchiveInfo, error) {
	if _, err := uuid.Parse(r.PathValue("id")); err != nil {
		return ArchiveInfo{}, errInvalidArchiveRun
	}
	return repository.ArchiveStatus(r.Context(), task.RunID(r.PathValue("id")))
}

var errInvalidArchiveRun = errors.New("执行记录标识无效")

func writeArchiveError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	status, message := http.StatusInternalServerError, "读取日志归档失败，请重试或下载完整日志"
	switch {
	case errors.Is(err, errInvalidArchiveRun):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, task.ErrRunNotFound), errors.Is(err, ErrArchiveNotFound):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, ErrArchiveOutdated):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, artifact.ErrObjectMissing):
		status, message = http.StatusServiceUnavailable, "归档对象暂不可用，请下载完整日志"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
	return true
}
