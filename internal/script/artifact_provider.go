package script

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/artifact"
)

type VersionArtifactProvider struct {
	db        *pgxpool.Pool
	artifacts artifact.Store
}

func NewVersionArtifactProvider(db *pgxpool.Pool, artifacts artifact.Store) *VersionArtifactProvider {
	return &VersionArtifactProvider{db: db, artifacts: artifacts}
}

func (p *VersionArtifactProvider) OpenVersionArtifact(ctx context.Context, serverID, versionID string) (io.ReadCloser, string, error) {
	var objectKey string
	var checksum string
	err := p.db.QueryRow(ctx, `
		SELECT version.artifact_uri, version.artifact_sha256
		FROM script_versions AS version
		JOIN script_syncs AS sync ON sync.script_version_id = version.id
		WHERE version.id = $1 AND sync.server_id = $2 AND sync.status = 'downloading'
	`, versionID, serverID).Scan(&objectKey, &checksum)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrVersionNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("读取脚本包元数据：%w", err)
	}
	body, err := p.artifacts.Open(ctx, objectKey)
	if err != nil {
		return nil, "", fmt.Errorf("打开脚本包：%w", err)
	}
	return body, checksum, nil
}
