package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/agentprotocol"
)

type PostgresRepository struct {
	db *pgxpool.Pool
}

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) LatestHeartbeatSequence(
	ctx context.Context,
	serverID string,
) (uint64, bool, error) {
	var sequence int64
	err := r.db.QueryRow(ctx, `SELECT last_heartbeat_sequence FROM servers WHERE id = $1`, serverID).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return uint64(sequence), true, nil
}

func (r *PostgresRepository) SaveHeartbeat(
	ctx context.Context,
	heartbeat agentprotocol.Heartbeat,
	receivedAt time.Time,
) (bool, error) {
	if heartbeat.Sequence > math.MaxInt64 {
		return false, ErrInvalidHeartbeat
	}
	runtimes, err := json.Marshal(heartbeat.Runtimes)
	if err != nil {
		return false, fmt.Errorf("编码运行环境：%w", err)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var serverID string
	err = tx.QueryRow(ctx, `
		UPDATE servers
		SET
			last_heartbeat_sequence = $2,
			last_seen_at = $3,
			status = CASE WHEN status IN ('pending', 'offline') THEN 'online' ELSE status END,
			runtimes = $4,
			agent_version = $5,
			updated_at = $3
		WHERE id = $1 AND last_heartbeat_sequence < $2
		RETURNING id
	`, heartbeat.ServerID, int64(heartbeat.Sequence), receivedAt, runtimes, heartbeat.AgentVersion).Scan(&serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	cpuUsagePercent := float64(0)
	if heartbeat.CPUTotalMilli > 0 {
		cpuUsagePercent = math.Min(float64(heartbeat.CPUUsedMilli)/float64(heartbeat.CPUTotalMilli)*100, 100)
	}
	memoryAvailable := max(heartbeat.MemoryTotalBytes-heartbeat.MemoryUsedBytes, 0)
	diskTotal := heartbeat.DiskTotalBytes
	if diskTotal < heartbeat.DiskFreeBytes {
		diskTotal = heartbeat.DiskFreeBytes
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO server_snapshots (
			server_id,
			cpu_usage_percent,
			memory_total_bytes,
			memory_available_bytes,
			disk_total_bytes,
			disk_available_bytes,
			running_tasks,
			collected_at,
			cpu_used_milli,
			memory_used_bytes,
			disk_free_bytes
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		serverID,
		cpuUsagePercent,
		heartbeat.MemoryTotalBytes,
		memoryAvailable,
		diskTotal,
		heartbeat.DiskFreeBytes,
		heartbeat.RunningTasks,
		receivedAt,
		heartbeat.CPUUsedMilli,
		heartbeat.MemoryUsedBytes,
		heartbeat.DiskFreeBytes,
	)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *PostgresRepository) MarkOfflineBefore(ctx context.Context, cutoff time.Time) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		UPDATE servers
		SET status = 'offline', updated_at = now()
		WHERE status IN ('online', 'draining') AND last_seen_at < $1
		RETURNING id
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var serverIDs []string
	for rows.Next() {
		var serverID string
		if err := rows.Scan(&serverID); err != nil {
			return nil, err
		}
		serverIDs = append(serverIDs, serverID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return serverIDs, nil
}

func (r *PostgresRepository) CreateEnrollmentToken(ctx context.Context, token EnrollmentTokenRecord) error {
	labels, err := json.Marshal(token.Labels)
	if err != nil {
		return fmt.Errorf("编码服务器标签：%w", err)
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO server_enrollment_tokens (
			id, token_hash, server_name, cloud_provider, region, labels, expires_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, token.ID, token.TokenHash, token.Name, token.CloudProvider, token.Region, labels, token.ExpiresAt, token.CreatedAt)
	return err
}

func (r *PostgresRepository) ConsumeEnrollmentToken(ctx context.Context, claim EnrollmentClaim) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tokenID string
	var name string
	var cloudProvider string
	var region string
	var labels []byte
	err = tx.QueryRow(ctx, `
		SELECT id, server_name, cloud_provider, region, labels
		FROM server_enrollment_tokens
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
		FOR UPDATE
	`, claim.TokenHash, claim.UsedAt).Scan(&tokenID, &name, &cloudProvider, &region, &labels)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO servers (id, name, cloud_provider, region, status, labels, last_seen_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'pending', $5, NULL, $6, $6)
	`, claim.ServerID, name, cloudProvider, region, labels, claim.UsedAt)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO agent_identities (id, server_id, credential_hash, created_at)
		VALUES ($1, $2, $3, $4)
	`, claim.IdentityID, claim.ServerID, claim.CredentialHash, claim.UsedAt)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE server_enrollment_tokens SET used_at = $1 WHERE id = $2`, claim.UsedAt, tokenID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *PostgresRepository) FindServerByCredentialHash(
	ctx context.Context,
	credentialHash []byte,
	authenticatedAt time.Time,
) (string, error) {
	var serverID string
	err := r.db.QueryRow(ctx, `
		UPDATE agent_identities
		SET last_authenticated_at = $2
		WHERE credential_hash = $1 AND revoked_at IS NULL
		RETURNING server_id
	`, credentialHash, authenticatedAt).Scan(&serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAgentCredentialInvalid
	}
	if err != nil {
		return "", err
	}
	return serverID, nil
}

var _ EnrollmentRepository = (*PostgresRepository)(nil)
var _ HeartbeatRepository = (*PostgresRepository)(nil)
