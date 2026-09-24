package runartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/artifact"
	"yunling.local/platform/internal/task"
)

var (
	ErrInvalid  = errors.New("产物名称、大小、校验和或发布策略不匹配")
	ErrAccess   = errors.New("此代理无权上传该运行的产物")
	ErrConflict = errors.New("同名产物已保存了不同内容")
	ErrLimit    = errors.New("产物超过本次运行的数量或总大小上限")
	ErrNotFound = errors.New("运行产物不存在")
)

type Record struct {
	ID        string    `json:"id"`
	RunID     string    `json:"runId"`
	Name      string    `json:"name"`
	ByteSize  int64     `json:"byteSize"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"createdAt"`
	ObjectKey string    `json:"-"`
}

type UploadInput struct {
	RunID, ServerID, ExecutionToken, Name, SHA256 string
	ByteSize                                      int64
}

type Service struct {
	db      *pgxpool.Pool
	objects artifact.Store
}

func NewService(db *pgxpool.Pool, objects artifact.Store) *Service {
	return &Service{db: db, objects: objects}
}

// Upload validates the immutable version policy before reading the body. A run
// row lock then serializes quota checks and indexing across concurrent uploads.
// Content-addressed storage makes retry after an object write/DB failure safe.
func (s *Service) Upload(ctx context.Context, input UploadInput, body io.Reader) (Record, bool, error) {
	if !validUpload(input) || body == nil {
		return Record{}, false, ErrInvalid
	}
	policy, err := uploadPolicy(ctx, s.db, input, false)
	if err != nil {
		return Record{}, false, err
	}
	if !policy.Allows(input.Name) || input.ByteSize > policy.MaxFileBytes {
		return Record{}, false, ErrInvalid
	}
	// Avoid keeping up to 100 MiB per concurrent request in API process memory.
	file, err := os.CreateTemp("", "yunling-run-artifact-*")
	if err != nil {
		return Record{}, false, fmt.Errorf("暂存运行产物：%w", err)
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, input.ByteSize+1))
	if err != nil {
		return Record{}, false, fmt.Errorf("读取或暂存运行产物：%w", err)
	}
	if written != input.ByteSize || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), input.SHA256) {
		return Record{}, false, ErrInvalid
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Record{}, false, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Record{}, false, err
	}
	defer tx.Rollback(ctx)
	policy, err = uploadPolicy(ctx, tx, input, true)
	if err != nil {
		return Record{}, false, err
	}
	if !policy.Allows(input.Name) || input.ByteSize > policy.MaxFileBytes {
		return Record{}, false, ErrInvalid
	}
	existing, err := scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+` FROM run_artifacts WHERE task_run_id=$1 AND name=$2`, input.RunID, input.Name))
	if err == nil {
		if existing.ByteSize != input.ByteSize || !strings.EqualFold(existing.SHA256, input.SHA256) {
			return Record{}, false, ErrConflict
		}
		return existing, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, err
	}
	var count int
	var total int64
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(sum(byte_size),0) FROM run_artifacts WHERE task_run_id=$1`, input.RunID).Scan(&count, &total); err != nil {
		return Record{}, false, err
	}
	if count >= agentprotocol.MaxArtifactFiles || input.ByteSize > policy.MaxTotalBytes-total {
		return Record{}, false, ErrLimit
	}
	checksum := strings.ToLower(input.SHA256)
	nameHash := sha256.Sum256([]byte(input.Name))
	key := "runs/" + input.RunID + "/artifacts/" + hex.EncodeToString(nameHash[:]) + "/" + checksum
	if err := s.objects.Put(ctx, key, file, input.ByteSize, checksum); err != nil {
		return Record{}, false, err
	}
	record, err := scanRecord(tx.QueryRow(ctx, `INSERT INTO run_artifacts(task_run_id,name,object_key,byte_size,sha256)
		VALUES($1,$2,$3,$4,$5) RETURNING `+recordColumns, input.RunID, input.Name, key, input.ByteSize, checksum))
	if err != nil {
		return Record{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func uploadPolicy(ctx context.Context, db rowQuerier, input UploadInput, lock bool) (*agentprotocol.ArtifactPolicy, error) {
	query := `SELECT version.manifest FROM task_runs AS run JOIN script_versions AS version ON version.id=run.script_version_id
		WHERE run.id=$1 AND run.assigned_server_id=$2 AND run.execution_token=$3`
	if lock {
		query += ` FOR UPDATE OF run`
	}
	var data []byte
	if err := db.QueryRow(ctx, query, input.RunID, input.ServerID, input.ExecutionToken).Scan(&data); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccess
	} else if err != nil {
		return nil, err
	}
	var manifest struct {
		Artifacts *agentprotocol.ArtifactPolicy `json:"artifacts"`
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.Artifacts == nil || manifest.Artifacts.Validate() != nil {
		return nil, ErrInvalid
	}
	return manifest.Artifacts, nil
}

func validUpload(input UploadInput) bool {
	checksum, err := hex.DecodeString(input.SHA256)
	return validID(input.RunID) && validID(input.ServerID) && input.ExecutionToken != "" && len(input.ExecutionToken) <= 256 &&
		input.Name != "" && len(input.Name) <= 255 && strings.IndexFunc(input.Name, unicode.IsControl) < 0 &&
		input.ByteSize >= 0 && input.ByteSize <= agentprotocol.MaxArtifactFileBytes && err == nil && len(checksum) == sha256.Size
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && len(value) == 36
}

const recordColumns = `id::text, task_run_id::text, name, byte_size, sha256, created_at, object_key`

func scanRecord(row pgx.Row) (Record, error) {
	var value Record
	err := row.Scan(&value.ID, &value.RunID, &value.Name, &value.ByteSize, &value.SHA256, &value.CreatedAt, &value.ObjectKey)
	return value, err
}

func (s *Service) List(ctx context.Context, runID string) ([]Record, error) {
	if !validID(runID) {
		return nil, ErrInvalid
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_runs WHERE id=$1)`, runID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, task.ErrRunNotFound
	}
	rows, err := s.db.Query(ctx, `SELECT `+recordColumns+` FROM run_artifacts WHERE task_run_id=$1 ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Record{}
	for rows.Next() {
		value, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Service) Open(ctx context.Context, runID, artifactID string) (Record, io.ReadCloser, error) {
	if !validID(runID) || !validID(artifactID) {
		return Record{}, nil, ErrInvalid
	}
	value, err := scanRecord(s.db.QueryRow(ctx, `SELECT `+recordColumns+` FROM run_artifacts WHERE task_run_id=$1 AND id=$2`, runID, artifactID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, nil, ErrNotFound
	}
	if err != nil {
		return Record{}, nil, err
	}
	body, err := s.objects.Open(ctx, value.ObjectKey)
	return value, body, err
}
