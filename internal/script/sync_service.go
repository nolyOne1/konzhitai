package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/agentprotocol"
)

var (
	ErrSyncNotFound      = errors.New("脚本同步记录不存在")
	ErrInvalidSyncResult = errors.New("脚本同步结果无效")
)

type SyncService struct {
	db              *pgxpool.Pool
	artifactBaseURL string
	now             func() time.Time
}

func NewSyncService(db *pgxpool.Pool, artifactBaseURL string, now func() time.Time) *SyncService {
	if now == nil {
		now = time.Now
	}
	return &SyncService{db: db, artifactBaseURL: strings.TrimRight(artifactBaseURL, "/"), now: now}
}

func (s *SyncService) PrepareVersion(ctx context.Context, versionID string) (int64, error) {
	var runtimeName string
	var manifestJSON []byte
	err := s.db.QueryRow(ctx, `
		SELECT script.runtime, version.manifest
		FROM script_versions AS version
		JOIN scripts AS script ON script.id = version.script_id
		WHERE version.id = $1
	`, versionID).Scan(&runtimeName, &manifestJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrVersionNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("读取待同步版本：%w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return 0, fmt.Errorf("解析待同步版本清单：%w", err)
	}
	if manifest.Runtime != "" {
		runtimeName = manifest.Runtime
	}

	where := "false"
	arguments := []any{versionID, runtimeName, s.now()}
	switch manifest.Distribution.Mode {
	case DistributionAllCompatible:
		where = "server.runtimes ? $2"
	case DistributionLabels:
		labels, err := json.Marshal(manifest.Distribution.Labels)
		if err != nil {
			return 0, fmt.Errorf("编码发布标签：%w", err)
		}
		arguments = append(arguments, labels)
		where = "server.runtimes ? $2 AND server.labels @> $4::jsonb"
	case DistributionServerGroup:
		arguments = append(arguments, manifest.Distribution.ServerGroupID)
		where = "server.runtimes ? $2 AND server.server_group_id = $4"
	case DistributionOnDemand:
		return 0, nil
	default:
		return 0, ErrInvalidDistribution
	}
	result, err := s.db.Exec(ctx, `
		INSERT INTO script_syncs (server_id, script_version_id, status, created_at, updated_at)
		SELECT server.id, $1, 'pending', $3, $3
		FROM servers AS server
		WHERE server.enabled = true
			AND server.status IN ('online', 'draining')
			AND `+where+`
		ON CONFLICT (server_id, script_version_id) DO NOTHING
	`, arguments...)
	if err != nil {
		return 0, fmt.Errorf("创建脚本同步记录：%w", err)
	}
	return result.RowsAffected(), nil
}

func (s *SyncService) NextCommand(ctx context.Context, serverID string) (agentprotocol.SyncCommand, bool, error) {
	if err := s.prepareServer(ctx, serverID); err != nil {
		return agentprotocol.SyncCommand{}, false, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return agentprotocol.SyncCommand{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var syncID string
	var command agentprotocol.SyncCommand
	err = tx.QueryRow(ctx, `
		SELECT sync.id, version.script_id, version.id, version.artifact_sha256
		FROM script_syncs AS sync
		JOIN script_versions AS version ON version.id = sync.script_version_id
		WHERE sync.server_id = $1 AND (
			sync.status IN ('pending', 'drifted')
			OR (sync.status = 'downloading' AND sync.updated_at < $2::timestamptz - interval '2 minutes')
		)
		ORDER BY CASE sync.status WHEN 'drifted' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END, sync.updated_at, sync.created_at
		FOR UPDATE OF sync SKIP LOCKED
		LIMIT 1
	`, serverID, s.now()).Scan(&syncID, &command.ScriptID, &command.VersionID, &command.SHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentprotocol.SyncCommand{}, false, nil
	}
	if err != nil {
		return agentprotocol.SyncCommand{}, false, fmt.Errorf("领取脚本同步任务：%w", err)
	}
	command.ArtifactURL = s.artifactBaseURL + "/api/agent/scripts/" + command.VersionID + "/artifact"
	if _, err := tx.Exec(ctx, `
		UPDATE script_syncs
		SET status = 'downloading', error_code = '', error_message = '', updated_at = $2
		WHERE id = $1
	`, syncID, s.now()); err != nil {
		return agentprotocol.SyncCommand{}, false, fmt.Errorf("更新脚本同步状态：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentprotocol.SyncCommand{}, false, err
	}
	return command, true, nil
}

func (s *SyncService) prepareServer(ctx context.Context, serverID string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO script_syncs (server_id, script_version_id, status, created_at, updated_at)
		SELECT server.id, latest.id, 'pending', $2, $2
		FROM servers AS server
		CROSS JOIN scripts AS script
		JOIN LATERAL (
			SELECT version.id, version.manifest
			FROM script_versions AS version
			WHERE version.script_id = script.id
			ORDER BY version.version DESC
			LIMIT 1
		) AS latest ON true
		WHERE server.id = $1 AND server.enabled = true
			AND server.status IN ('online', 'draining')
			AND server.runtimes ? COALESCE(NULLIF(latest.manifest->>'runtime', ''), script.runtime)
			AND (
				latest.manifest->'distribution'->>'mode' = 'all_compatible'
				OR (
					latest.manifest->'distribution'->>'mode' = 'labels'
					AND server.labels @> COALESCE(latest.manifest->'distribution'->'labels', '{}'::jsonb)
				)
				OR (
					latest.manifest->'distribution'->>'mode' = 'server_group'
					AND server.server_group_id = COALESCE(latest.manifest->'distribution'->>'serverGroupId', '')
				)
			)
		ON CONFLICT (server_id, script_version_id) DO NOTHING
	`, serverID, s.now())
	if err != nil {
		return fmt.Errorf("为上线服务器准备共享脚本：%w", err)
	}
	return nil
}

func (s *SyncService) RecordResult(ctx context.Context, serverID string, result agentprotocol.SyncResult) error {
	if result.ScriptID == "" || result.VersionID == "" || !validResultState(result.State) {
		return ErrInvalidSyncResult
	}
	var expectedSHA string
	err := s.db.QueryRow(ctx, `
		SELECT version.artifact_sha256
		FROM script_syncs AS sync
		JOIN script_versions AS version ON version.id = sync.script_version_id
		WHERE sync.server_id = $1 AND version.id = $2 AND version.script_id = $3
	`, serverID, result.VersionID, result.ScriptID).Scan(&expectedSHA)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSyncNotFound
	}
	if err != nil {
		return fmt.Errorf("读取脚本同步记录：%w", err)
	}
	state := result.State
	errorCode := strings.TrimSpace(result.ErrorCode)
	errorMessage := strings.TrimSpace(result.ErrorMessage)
	artifactSHA := ""
	var syncedAt any
	if state == agentprotocol.SyncReady {
		if !strings.EqualFold(result.SHA256, expectedSHA) {
			state = agentprotocol.SyncFailed
			errorCode = "checksum_mismatch"
			errorMessage = "服务器返回的脚本校验值与中央版本不一致"
		} else {
			artifactSHA = strings.ToLower(expectedSHA)
			syncedAt = s.now()
		}
	}
	command, err := s.db.Exec(ctx, `
		UPDATE script_syncs AS sync
		SET status = $4, artifact_sha256 = NULLIF($5, ''), error_code = $6,
			error_message = $7, synced_at = $8, updated_at = $9
		FROM script_versions AS version
		WHERE sync.server_id = $1 AND sync.script_version_id = version.id
			AND version.id = $2 AND version.script_id = $3
	`, serverID, result.VersionID, result.ScriptID, state, artifactSHA, errorCode, errorMessage, syncedAt, s.now())
	if err != nil {
		return fmt.Errorf("记录脚本同步结果：%w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrSyncNotFound
	}
	return nil
}

func (s *SyncService) List(ctx context.Context, scriptID string) ([]SyncView, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sync.id, server.id, server.name, version.script_id, version.id, version.version,
			sync.status, version.artifact_sha256, sync.error_code, sync.error_message,
			sync.synced_at, sync.updated_at
		FROM script_syncs AS sync
		JOIN script_versions AS version ON version.id = sync.script_version_id
		JOIN servers AS server ON server.id = sync.server_id
		WHERE version.script_id = $1
		ORDER BY version.version DESC, server.name
	`, scriptID)
	if err != nil {
		return nil, fmt.Errorf("读取脚本同步状态：%w", err)
	}
	defer rows.Close()
	items := make([]SyncView, 0)
	for rows.Next() {
		var item SyncView
		if err := rows.Scan(&item.ID, &item.ServerID, &item.ServerName, &item.ScriptID, &item.VersionID,
			&item.VersionNumber, &item.State, &item.ArtifactSHA256, &item.ErrorCode, &item.ErrorMessage,
			&item.SyncedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("解析脚本同步状态：%w", err)
		}
		item.Blocked = item.State != agentprotocol.SyncReady
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SyncService) Retry(ctx context.Context, syncID string) error {
	command, err := s.db.Exec(ctx, `
		UPDATE script_syncs
		SET status = 'pending', error_code = '', error_message = '', synced_at = NULL, updated_at = $2
		WHERE id = $1 AND status IN ('failed', 'drifted')
	`, syncID, s.now())
	if err != nil {
		return fmt.Errorf("重试脚本同步：%w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrSyncNotFound
	}
	return nil
}

func validResultState(state agentprotocol.SyncState) bool {
	return state == agentprotocol.SyncReady || state == agentprotocol.SyncFailed || state == agentprotocol.SyncDrifted
}
