package agentrelease

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ db *pgxpool.Pool }

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository { return &PostgresRepository{db: db} }

func (r *PostgresRepository) Create(ctx context.Context, release Release) (Release, error) {
	capabilities, err := json.Marshal(nonNilStrings(release.Capabilities))
	if err != nil {
		return Release{}, fmt.Errorf("编码代理版本能力：%w", err)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if release.Status == "" {
		release.Status = ReleaseStatusAvailable
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO agent_releases (version, status, recommended, release_notes, manifest_sha256, capabilities, created_by)
		VALUES ($1, $2, false, $3, $4, $5, NULLIF($6, '')::uuid)
		RETURNING id, created_at
	`, release.Version, release.Status, release.ReleaseNotes, release.ManifestSHA256, capabilities, release.CreatedBy).Scan(&release.ID, &release.CreatedAt)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23505" {
			return Release{}, ErrReleaseExists
		}
		return Release{}, err
	}
	release.Recommended = false
	for index := range release.Artifacts {
		item := &release.Artifacts[index]
		err = tx.QueryRow(ctx, `
			INSERT INTO agent_release_artifacts (release_id, os, arch, file_name, byte_size, sha256, object_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id
		`, release.ID, item.OS, item.Arch, item.FileName, item.ByteSize, item.SHA256, item.ObjectKey).Scan(&item.ID)
		if err != nil {
			return Release{}, err
		}
	}
	if release.CreatedBy != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_logs(actor_id, action, target_type, target_id, details)
			VALUES ($1::uuid, 'agent_release.import', 'agent_release', $2, jsonb_build_object('version', $3::text))
		`, release.CreatedBy, release.ID, release.Version); err != nil {
			return Release{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Release{}, err
	}
	return publicRelease(release), nil
}

func (r *PostgresRepository) List(ctx context.Context) ([]Release, error) {
	rows, err := r.db.Query(ctx, releaseSelect+` ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var releases []Release
	for rows.Next() {
		release, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		release.Artifacts, err = r.artifacts(ctx, release.ID)
		if err != nil {
			return nil, err
		}
		releases = append(releases, publicRelease(release))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if releases == nil {
		releases = []Release{}
	}
	return releases, nil
}

func (r *PostgresRepository) Recommended(ctx context.Context) (Release, error) {
	return r.releaseByQuery(ctx, releaseSelect+` WHERE recommended = true`)
}

func (r *PostgresRepository) SetRecommended(ctx context.Context, id string) (Release, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE agent_releases IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return Release{}, err
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM agent_releases WHERE id = $1 FOR UPDATE`, id).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return Release{}, ErrReleaseNotFound
	} else if err != nil {
		return Release{}, err
	}
	if status == ReleaseStatusWithdrawn {
		return Release{}, ErrReleaseWithdrawn
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_releases SET recommended = false WHERE recommended = true AND id <> $1`, id); err != nil {
		return Release{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_releases SET recommended = true WHERE id = $1`, id); err != nil {
		return Release{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Release{}, err
	}
	return r.releaseByID(ctx, id)
}

func (r *PostgresRepository) Withdraw(ctx context.Context, id string) (Release, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var recommended bool
	err = tx.QueryRow(ctx, `SELECT recommended FROM agent_releases WHERE id = $1 FOR UPDATE`, id).Scan(&recommended)
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, ErrReleaseNotFound
	}
	if err != nil {
		return Release{}, err
	}
	if recommended {
		return Release{}, ErrRecommendedRelease
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_releases SET status = 'withdrawn' WHERE id = $1`, id); err != nil {
		return Release{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Release{}, err
	}
	return r.releaseByID(ctx, id)
}

func (r *PostgresRepository) FindArtifact(ctx context.Context, version, digest, fileName string) (Release, Artifact, error) {
	var release Release
	var capabilities []byte
	var createdBy sql.NullString
	var item Artifact
	err := r.db.QueryRow(ctx, `
		SELECT release.id, release.version, release.status, release.recommended, release.release_notes,
		       release.manifest_sha256, release.capabilities, release.created_by::text, release.created_at,
		       artifact.id, artifact.os, artifact.arch, artifact.file_name, artifact.byte_size, artifact.sha256, artifact.object_key
		FROM agent_releases AS release
		JOIN agent_release_artifacts AS artifact ON artifact.release_id = release.id
		WHERE release.version = $1 AND artifact.sha256 = $2 AND artifact.file_name = $3
	`, version, digest, fileName).Scan(
		&release.ID, &release.Version, &release.Status, &release.Recommended, &release.ReleaseNotes,
		&release.ManifestSHA256, &capabilities, &createdBy, &release.CreatedAt,
		&item.ID, &item.OS, &item.Arch, &item.FileName, &item.ByteSize, &item.SHA256, &item.ObjectKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, Artifact{}, ErrArtifactNotFound
	}
	if err != nil {
		return Release{}, Artifact{}, err
	}
	if err := json.Unmarshal(capabilities, &release.Capabilities); err != nil {
		return Release{}, Artifact{}, err
	}
	if createdBy.Valid {
		release.CreatedBy = createdBy.String
	}
	item.DownloadURL = artifactDownloadURL(release.Version, item)
	release.Artifacts = []Artifact{item}
	return release, item, nil
}

const releaseSelect = `SELECT id, version, status, recommended, release_notes, manifest_sha256, capabilities, created_by::text, created_at FROM agent_releases`

type releaseRow interface{ Scan(...any) error }

func scanRelease(row releaseRow) (Release, error) {
	var release Release
	var capabilities []byte
	var createdBy sql.NullString
	if err := row.Scan(&release.ID, &release.Version, &release.Status, &release.Recommended, &release.ReleaseNotes, &release.ManifestSHA256, &capabilities, &createdBy, &release.CreatedAt); err != nil {
		return Release{}, err
	}
	if err := json.Unmarshal(capabilities, &release.Capabilities); err != nil {
		return Release{}, fmt.Errorf("解析代理版本能力：%w", err)
	}
	if release.Capabilities == nil {
		release.Capabilities = []string{}
	}
	if createdBy.Valid {
		release.CreatedBy = createdBy.String
	}
	return release, nil
}

func (r *PostgresRepository) releaseByQuery(ctx context.Context, query string, arguments ...any) (Release, error) {
	release, err := scanRelease(r.db.QueryRow(ctx, query, arguments...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, ErrReleaseNotFound
	}
	if err != nil {
		return Release{}, err
	}
	release.Artifacts, err = r.artifacts(ctx, release.ID)
	if err != nil {
		return Release{}, err
	}
	return publicRelease(release), nil
}

func (r *PostgresRepository) releaseByID(ctx context.Context, id string) (Release, error) {
	return r.releaseByQuery(ctx, releaseSelect+` WHERE id = $1`, id)
}

func (r *PostgresRepository) artifacts(ctx context.Context, releaseID string) ([]Artifact, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, os, arch, file_name, byte_size, sha256, object_key
		FROM agent_release_artifacts WHERE release_id = $1
		ORDER BY CASE arch WHEN 'amd64' THEN 1 ELSE 2 END
	`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Artifact{}
	for rows.Next() {
		var item Artifact
		if err := rows.Scan(&item.ID, &item.OS, &item.Arch, &item.FileName, &item.ByteSize, &item.SHA256, &item.ObjectKey); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
