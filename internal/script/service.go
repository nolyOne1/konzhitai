package script

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/artifact"
)

const MaxScriptBytes int64 = 1 << 20

var (
	ErrScriptNotFound        = errors.New("脚本不存在")
	ErrVersionNotFound       = errors.New("脚本版本不存在")
	ErrInvalidScript         = errors.New("脚本信息不完整")
	ErrInvalidDraft          = errors.New("脚本草稿内容无效")
	ErrInvalidPublish        = errors.New("发布信息不完整")
	ErrInvalidReleaseNotes   = errors.New("发布说明必须包含中文")
	ErrInvalidDistribution   = errors.New("请选择有效的发布目标")
	ErrScriptContentTooLarge = errors.New("脚本内容不能超过 1 MB")
)

type CreateInput struct {
	Name        string
	Description string
	Runtime     string
	Entrypoint  string
	Category    string
	Tags        []string
	Content     []byte
	AuthorID    string
}

type DraftInput struct {
	ScriptID             string
	Content              []byte
	Runtime              string
	Entrypoint           string
	Category             string
	Tags                 []string
	Distribution         DistributionRule
	ParameterDefinitions []ParameterDefinition
	Resources            ResourceRequirements
	AuthorID             string
}

type PublishInput struct {
	ScriptID             string
	Content              []byte
	Runtime              string
	Entrypoint           string
	Category             string
	Tags                 []string
	ReleaseNotes         string
	Distribution         DistributionRule
	ParameterDefinitions []ParameterDefinition
	Resources            ResourceRequirements
	AuthorID             string
}

type RollbackInput struct {
	ScriptID     string
	VersionID    string
	ReleaseNotes string
	AuthorID     string
}

type Detail struct {
	Script   Script    `json:"script"`
	Draft    Draft     `json:"draft"`
	Versions []Version `json:"versions"`
}

type Service struct {
	db        *pgxpool.Pool
	artifacts artifact.Store
	now       func() time.Time
}

func NewService(db *pgxpool.Pool, artifacts artifact.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: db, artifacts: artifacts, now: now}
}

func (s *Service) List(ctx context.Context) ([]Script, error) {
	rows, err := s.db.Query(ctx, scriptSelect+` ORDER BY script.updated_at DESC, script.name`)
	if err != nil {
		return nil, fmt.Errorf("读取脚本列表：%w", err)
	}
	defer rows.Close()
	scripts := make([]Script, 0)
	for rows.Next() {
		item, err := scanScript(rows)
		if err != nil {
			return nil, fmt.Errorf("解析脚本列表：%w", err)
		}
		scripts = append(scripts, item)
	}
	return scripts, rows.Err()
}

func (s *Service) Get(ctx context.Context, id string) (Detail, error) {
	item, err := scanScript(s.db.QueryRow(ctx, scriptSelect+` WHERE script.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrScriptNotFound
	}
	if err != nil {
		return Detail{}, fmt.Errorf("读取脚本：%w", err)
	}
	var draft Draft
	var manifestJSON []byte
	var baseVersionID *string
	err = s.db.QueryRow(ctx, `
		SELECT script_id, base_version_id, content, manifest, updated_at
		FROM script_drafts
		WHERE script_id = $1
	`, id).Scan(&draft.ScriptID, &baseVersionID, &draft.Content, &manifestJSON, &draft.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrScriptNotFound
	}
	if err != nil {
		return Detail{}, fmt.Errorf("读取脚本草稿：%w", err)
	}
	if baseVersionID != nil {
		draft.BaseVersionID = *baseVersionID
	}
	if err := json.Unmarshal(manifestJSON, &draft.Manifest); err != nil {
		return Detail{}, fmt.Errorf("解析脚本草稿清单：%w", err)
	}
	versions, err := s.ListVersions(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Script: item, Draft: draft, Versions: versions}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Script, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Runtime = normalizeRuntime(input.Runtime)
	input.Entrypoint = strings.TrimSpace(input.Entrypoint)
	if input.Entrypoint == "" {
		input.Entrypoint = defaultEntrypoint(input.Runtime)
	}
	if input.Name == "" || !validRuntime(input.Runtime) || !validEntrypoint(input.Entrypoint) || int64(len(input.Content)) > MaxScriptBytes {
		return Script{}, ErrInvalidScript
	}
	manifest := withResourceDefaults(Manifest{
		Runtime:      input.Runtime,
		Entrypoint:   input.Entrypoint,
		Category:     strings.TrimSpace(input.Category),
		Tags:         normalizeTags(input.Tags),
		Distribution: DistributionRule{Mode: DistributionOnDemand},
	})
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		return Script{}, fmt.Errorf("编码脚本清单：%w", err)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Script{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var created Script
	err = tx.QueryRow(ctx, `
		INSERT INTO scripts (name, description, runtime, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		RETURNING id, name, description, runtime, created_at, updated_at
	`, input.Name, input.Description, input.Runtime, nullableUUID(input.AuthorID), s.now()).Scan(
		&created.ID, &created.Name, &created.Description, &created.Runtime, &created.CreatedAt, &created.UpdatedAt,
	)
	if err != nil {
		return Script{}, fmt.Errorf("创建脚本：%w", err)
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO script_drafts (script_id, content, manifest, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		RETURNING updated_at
	`, created.ID, string(input.Content), encodedManifest, nullableUUID(input.AuthorID), s.now()).Scan(&created.DraftUpdatedAt)
	if err != nil {
		return Script{}, fmt.Errorf("创建脚本草稿：%w", err)
	}
	if err := insertAudit(ctx, tx, input.AuthorID, "script.create", created.ID, map[string]any{"name": created.Name}); err != nil {
		return Script{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Script{}, err
	}
	return created, nil
}

func (s *Service) SaveDraft(ctx context.Context, input DraftInput) (Draft, error) {
	manifest := withResourceDefaults(Manifest{
		Runtime:              normalizeRuntime(input.Runtime),
		Entrypoint:           strings.TrimSpace(input.Entrypoint),
		Category:             strings.TrimSpace(input.Category),
		Tags:                 normalizeTags(input.Tags),
		Distribution:         normalizeDistribution(input.Distribution),
		ParameterDefinitions: input.ParameterDefinitions,
		Resources:            input.Resources,
	})
	if strings.TrimSpace(input.ScriptID) == "" || len(input.Content) == 0 || int64(len(input.Content)) > MaxScriptBytes ||
		!validRuntime(manifest.Runtime) || !validEntrypoint(manifest.Entrypoint) || validateDistribution(manifest.Distribution) != nil {
		return Draft{}, ErrInvalidDraft
	}
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		return Draft{}, fmt.Errorf("编码脚本清单：%w", err)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Draft{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedID string
	if err := tx.QueryRow(ctx, `SELECT id FROM scripts WHERE id = $1 FOR UPDATE`, input.ScriptID).Scan(&lockedID); errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrScriptNotFound
	} else if err != nil {
		return Draft{}, fmt.Errorf("锁定脚本草稿：%w", err)
	}
	var draft Draft
	var baseVersionID *string
	err = tx.QueryRow(ctx, `
		INSERT INTO script_drafts (script_id, content, manifest, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (script_id) DO UPDATE SET
			content = EXCLUDED.content,
			manifest = EXCLUDED.manifest,
			updated_by = EXCLUDED.updated_by,
			updated_at = EXCLUDED.updated_at
		RETURNING script_id, base_version_id, content, manifest, updated_at
	`, input.ScriptID, string(input.Content), encodedManifest, nullableUUID(input.AuthorID), s.now()).Scan(
		&draft.ScriptID, &baseVersionID, &draft.Content, &encodedManifest, &draft.UpdatedAt,
	)
	if err != nil {
		return Draft{}, fmt.Errorf("保存脚本草稿：%w", err)
	}
	if baseVersionID != nil {
		draft.BaseVersionID = *baseVersionID
	}
	if err := json.Unmarshal(encodedManifest, &draft.Manifest); err != nil {
		return Draft{}, fmt.Errorf("解析脚本清单：%w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE scripts SET runtime = $2, updated_at = $3 WHERE id = $1`, input.ScriptID, manifest.Runtime, s.now())
	if err != nil {
		return Draft{}, fmt.Errorf("更新脚本运行环境：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (s *Service) Publish(ctx context.Context, input PublishInput) (Version, error) {
	return s.publish(ctx, input, "script.publish", "")
}

func (s *Service) publish(ctx context.Context, input PublishInput, auditAction, sourceVersionID string) (Version, error) {
	input.ScriptID = strings.TrimSpace(input.ScriptID)
	input.Runtime = normalizeRuntime(input.Runtime)
	input.Entrypoint = strings.TrimSpace(input.Entrypoint)
	input.ReleaseNotes = strings.TrimSpace(input.ReleaseNotes)
	input.Distribution = normalizeDistribution(input.Distribution)
	if input.ScriptID == "" || len(input.Content) == 0 || input.Entrypoint == "" || !validRuntime(input.Runtime) || !validEntrypoint(input.Entrypoint) {
		return Version{}, ErrInvalidPublish
	}
	if int64(len(input.Content)) > MaxScriptBytes {
		return Version{}, ErrScriptContentTooLarge
	}
	if !containsHan(input.ReleaseNotes) {
		return Version{}, ErrInvalidReleaseNotes
	}
	if err := validateDistribution(input.Distribution); err != nil {
		return Version{}, err
	}
	manifest := withResourceDefaults(Manifest{
		Runtime:              input.Runtime,
		Entrypoint:           input.Entrypoint,
		Category:             strings.TrimSpace(input.Category),
		Tags:                 normalizeTags(input.Tags),
		Distribution:         input.Distribution,
		ParameterDefinitions: input.ParameterDefinitions,
		Resources:            input.Resources,
	})
	archive, err := buildArchive(input.Content, manifest)
	if err != nil {
		return Version{}, err
	}
	digest := sha256.Sum256(archive)
	digestHex := hex.EncodeToString(digest[:])
	objectKey := fmt.Sprintf("scripts/%s/%s.tar.gz", input.ScriptID, digestHex)
	if err := s.artifacts.Put(ctx, objectKey, bytes.NewReader(archive), int64(len(archive)), digestHex); err != nil {
		return Version{}, fmt.Errorf("保存脚本包：%w", err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return Version{}, fmt.Errorf("编码脚本清单：%w", err)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Version{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedID string
	if err := tx.QueryRow(ctx, `SELECT id FROM scripts WHERE id = $1 FOR UPDATE`, input.ScriptID).Scan(&lockedID); errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrScriptNotFound
	} else if err != nil {
		return Version{}, err
	}
	var version Version
	var createdBy *string
	err = tx.QueryRow(ctx, `
		INSERT INTO script_versions (
			script_id, version, artifact_uri, artifact_sha256, entrypoint,
			manifest, release_notes, created_by, created_at
		)
		SELECT $1, COALESCE(MAX(version), 0) + 1, $2, $3, $4, $5, $6, $7, $8
		FROM script_versions
		WHERE script_id = $1
		RETURNING id, script_id, version, artifact_uri, artifact_sha256, entrypoint,
			manifest, release_notes, created_by, created_at
	`, input.ScriptID, objectKey, digestHex, input.Entrypoint, manifestJSON, input.ReleaseNotes, nullableUUID(input.AuthorID), s.now()).Scan(
		&version.ID, &version.ScriptID, &version.Number, &version.ArtifactURI,
		&version.ArtifactSHA256, &version.Entrypoint, &manifestJSON,
		&version.ReleaseNotes, &createdBy, &version.CreatedAt,
	)
	if err != nil {
		return Version{}, fmt.Errorf("创建脚本版本：%w", err)
	}
	if createdBy != nil {
		version.CreatedBy = *createdBy
	}
	version.Manifest = manifest
	_, err = tx.Exec(ctx, `
		INSERT INTO script_drafts (script_id, base_version_id, content, manifest, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
		ON CONFLICT (script_id) DO UPDATE SET
			base_version_id = EXCLUDED.base_version_id,
			content = EXCLUDED.content,
			manifest = EXCLUDED.manifest,
			updated_by = EXCLUDED.updated_by,
			updated_at = EXCLUDED.updated_at
	`, input.ScriptID, version.ID, string(input.Content), manifestJSON, nullableUUID(input.AuthorID), s.now())
	if err != nil {
		return Version{}, fmt.Errorf("更新发布后的草稿：%w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE scripts SET runtime = $2, updated_at = $3 WHERE id = $1`, input.ScriptID, input.Runtime, s.now())
	if err != nil {
		return Version{}, fmt.Errorf("更新脚本：%w", err)
	}
	details := map[string]any{"version": version.Number, "sha256": digestHex, "distribution": input.Distribution}
	if sourceVersionID != "" {
		details["sourceVersionId"] = sourceVersionID
	}
	if err := insertAudit(ctx, tx, input.AuthorID, auditAction, input.ScriptID, details); err != nil {
		return Version{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Version{}, err
	}
	return version, nil
}

func (s *Service) Rollback(ctx context.Context, input RollbackInput) (Version, error) {
	var version Version
	var manifestJSON []byte
	var createdBy *string
	err := s.db.QueryRow(ctx, `
		SELECT id, script_id, version, artifact_uri, artifact_sha256, entrypoint,
			manifest, release_notes, created_by, created_at
		FROM script_versions
		WHERE id = $1 AND script_id = $2
	`, input.VersionID, input.ScriptID).Scan(
		&version.ID, &version.ScriptID, &version.Number, &version.ArtifactURI,
		&version.ArtifactSHA256, &version.Entrypoint, &manifestJSON,
		&version.ReleaseNotes, &createdBy, &version.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrVersionNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("读取回滚版本：%w", err)
	}
	if err := json.Unmarshal(manifestJSON, &version.Manifest); err != nil {
		return Version{}, fmt.Errorf("解析历史脚本清单：%w", err)
	}
	body, err := s.artifacts.Open(ctx, version.ArtifactURI)
	if err != nil {
		return Version{}, fmt.Errorf("读取历史脚本包：%w", err)
	}
	defer body.Close()
	archive, err := io.ReadAll(io.LimitReader(body, MaxScriptBytes*2+1))
	if err != nil {
		return Version{}, fmt.Errorf("读取历史脚本包：%w", err)
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != version.ArtifactSHA256 {
		return Version{}, errors.New("历史脚本包校验失败")
	}
	content, archivedManifest, err := readArchive(archive, version.Entrypoint)
	if err != nil {
		return Version{}, err
	}
	return s.publish(ctx, PublishInput{
		ScriptID:             input.ScriptID,
		Content:              content,
		Runtime:              archivedManifest.Runtime,
		Entrypoint:           archivedManifest.Entrypoint,
		Category:             archivedManifest.Category,
		Tags:                 archivedManifest.Tags,
		ReleaseNotes:         input.ReleaseNotes,
		Distribution:         archivedManifest.Distribution,
		ParameterDefinitions: archivedManifest.ParameterDefinitions,
		Resources:            archivedManifest.Resources,
		AuthorID:             input.AuthorID,
	}, "script.rollback", input.VersionID)
}

func (s *Service) ListVersions(ctx context.Context, scriptID string) ([]Version, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, script_id, version, artifact_uri, artifact_sha256, entrypoint,
			manifest, release_notes, created_by, created_at
		FROM script_versions
		WHERE script_id = $1
		ORDER BY version DESC
	`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := make([]Version, 0)
	for rows.Next() {
		var version Version
		var manifestJSON []byte
		var createdBy *string
		if err := rows.Scan(
			&version.ID, &version.ScriptID, &version.Number, &version.ArtifactURI,
			&version.ArtifactSHA256, &version.Entrypoint, &manifestJSON,
			&version.ReleaseNotes, &createdBy, &version.CreatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(manifestJSON, &version.Manifest); err != nil {
			return nil, err
		}
		if createdBy != nil {
			version.CreatedBy = *createdBy
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func (s *Service) VersionContent(ctx context.Context, scriptID, versionID string) (string, error) {
	var artifactURI string
	var checksum string
	var entrypoint string
	err := s.db.QueryRow(ctx, `
		SELECT artifact_uri, artifact_sha256, entrypoint
		FROM script_versions
		WHERE id = $1 AND script_id = $2
	`, versionID, scriptID).Scan(&artifactURI, &checksum, &entrypoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrVersionNotFound
	}
	if err != nil {
		return "", fmt.Errorf("读取脚本版本：%w", err)
	}
	body, err := s.artifacts.Open(ctx, artifactURI)
	if err != nil {
		return "", fmt.Errorf("读取脚本包：%w", err)
	}
	defer body.Close()
	archive, err := io.ReadAll(io.LimitReader(body, MaxScriptBytes*2+1))
	if err != nil {
		return "", fmt.Errorf("读取脚本包：%w", err)
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != checksum {
		return "", errors.New("脚本包校验失败")
	}
	content, _, err := readArchive(archive, entrypoint)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

const scriptSelect = `
	SELECT
		script.id,
		script.name,
		script.description,
		script.runtime,
		COALESCE(latest.id::text, ''),
		COALESCE(latest.version, 0),
		draft.updated_at,
		COALESCE(NULLIF(draft.manifest->>'category', ''), '未分类'),
		COALESCE(draft.manifest->'tags', '[]'::jsonb),
		script.created_at,
		script.updated_at
	FROM scripts AS script
	LEFT JOIN LATERAL (
		SELECT id, version
		FROM script_versions
		WHERE script_id = script.id
		ORDER BY version DESC
		LIMIT 1
	) AS latest ON true
	LEFT JOIN script_drafts AS draft ON draft.script_id = script.id
`

type rowScanner interface {
	Scan(...any) error
}

func scanScript(row rowScanner) (Script, error) {
	var item Script
	var tagsJSON []byte
	err := row.Scan(
		&item.ID,
		&item.Name,
		&item.Description,
		&item.Runtime,
		&item.CurrentVersionID,
		&item.CurrentVersion,
		&item.DraftUpdatedAt,
		&item.Category,
		&tagsJSON,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err == nil {
		err = json.Unmarshal(tagsJSON, &item.Tags)
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	return item, err
}

func buildArchive(content []byte, manifest Manifest) ([]byte, error) {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("编码脚本清单：%w", err)
	}
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	compressed.Name = ""
	compressed.Comment = ""
	compressed.ModTime = time.Time{}
	archive := tar.NewWriter(compressed)
	files := []struct {
		name string
		mode int64
		body []byte
	}{
		{name: manifest.Entrypoint, mode: 0o750, body: content},
		{name: "manifest.json", mode: 0o640, body: manifestJSON},
	}
	for _, file := range files {
		header := &tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.body)), ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR}
		if err := archive.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("写入脚本包头：%w", err)
		}
		if _, err := archive.Write(file.body); err != nil {
			return nil, fmt.Errorf("写入脚本包内容：%w", err)
		}
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("结束脚本包：%w", err)
	}
	if err := compressed.Close(); err != nil {
		return nil, fmt.Errorf("压缩脚本包：%w", err)
	}
	return output.Bytes(), nil
}

func readArchive(archiveBytes []byte, entrypoint string) ([]byte, Manifest, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(archiveBytes))
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("解压脚本包：%w", err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	var content []byte
	var manifest Manifest
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, Manifest{}, fmt.Errorf("读取脚本包：%w", err)
		}
		if header.Size > MaxScriptBytes {
			return nil, Manifest{}, ErrScriptContentTooLarge
		}
		contents, err := io.ReadAll(io.LimitReader(reader, MaxScriptBytes+1))
		if err != nil {
			return nil, Manifest{}, fmt.Errorf("读取脚本包文件：%w", err)
		}
		switch header.Name {
		case entrypoint:
			content = contents
		case "manifest.json":
			if err := json.Unmarshal(contents, &manifest); err != nil {
				return nil, Manifest{}, fmt.Errorf("解析脚本包清单：%w", err)
			}
		}
	}
	if len(content) == 0 || manifest.Entrypoint != entrypoint {
		return nil, Manifest{}, errors.New("脚本包内容不完整")
	}
	return content, manifest, nil
}

func insertAudit(ctx context.Context, tx pgx.Tx, actorID, action, targetID string, details map[string]any) error {
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_logs (actor_id, action, target_type, target_id, details)
		VALUES ($1, $2, 'script', $3, $4)
	`, nullableUUID(actorID), action, targetID, encoded)
	if err != nil {
		return fmt.Errorf("写入脚本审计记录：%w", err)
	}
	return nil
}

func normalizeRuntime(runtime string) string {
	return strings.ToLower(strings.TrimSpace(runtime))
}

func validRuntime(runtime string) bool {
	switch runtime {
	case "bash", "python3", "node", "powershell":
		return true
	default:
		return false
	}
}

func defaultEntrypoint(runtime string) string {
	switch runtime {
	case "python3":
		return "main.py"
	case "node":
		return "main.js"
	case "powershell":
		return "main.ps1"
	default:
		return "main.sh"
	}
}

func validEntrypoint(entrypoint string) bool {
	cleaned := path.Clean(entrypoint)
	return entrypoint != "" && cleaned == entrypoint && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "/") && !strings.HasPrefix(cleaned, "../") && !strings.Contains(cleaned, "\\")
}

func normalizeDistribution(rule DistributionRule) DistributionRule {
	if rule.Labels == nil {
		rule.Labels = map[string]string{}
	}
	rule.ServerGroupID = strings.TrimSpace(rule.ServerGroupID)
	return rule
}

func validateDistribution(rule DistributionRule) error {
	switch rule.Mode {
	case DistributionAllCompatible, DistributionOnDemand:
		return nil
	case DistributionServerGroup:
		if rule.ServerGroupID != "" {
			return nil
		}
	case DistributionLabels:
		if len(rule.Labels) > 0 {
			for key := range rule.Labels {
				if strings.TrimSpace(key) == "" {
					return ErrInvalidDistribution
				}
			}
			return nil
		}
	}
	return ErrInvalidDistribution
}

func withResourceDefaults(manifest Manifest) Manifest {
	manifest.Distribution = normalizeDistribution(manifest.Distribution)
	if manifest.Resources.CPUMillicores <= 0 {
		manifest.Resources.CPUMillicores = 100
	}
	if manifest.Resources.MemoryBytes <= 0 {
		manifest.Resources.MemoryBytes = 128 << 20
	}
	if manifest.Resources.DiskBytes <= 0 {
		manifest.Resources.DiskBytes = 128 << 20
	}
	if manifest.ParameterDefinitions == nil {
		manifest.ParameterDefinitions = []ParameterDefinition{}
	}
	manifest.Tags = normalizeTags(manifest.Tags)
	if strings.TrimSpace(manifest.Category) == "" {
		manifest.Category = "未分类"
	}
	return manifest
}

func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	normalized := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			normalized = append(normalized, tag)
		}
	}
	return normalized
}

func containsHan(value string) bool {
	for _, character := range value {
		if unicode.Is(unicode.Han, character) {
			return true
		}
	}
	return false
}

func nullableUUID(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
