package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
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
			status = CASE
				WHEN status = 'quarantined' THEN status
				WHEN drain_requested THEN 'draining'
				ELSE 'online'
			END,
			runtimes = $4,
			agent_version = $5,
			updated_at = $3
		WHERE id = $1 AND enabled = true AND last_heartbeat_sequence < $2
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
			cpu_total_milli,
			memory_total_bytes,
			memory_available_bytes,
			disk_total_bytes,
			disk_available_bytes,
			running_tasks,
			collected_at,
			cpu_used_milli,
			memory_used_bytes,
			disk_free_bytes
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`,
		serverID,
		cpuUsagePercent,
		heartbeat.CPUTotalMilli,
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
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var identityID, serverID string
	var pending bool
	err = tx.QueryRow(ctx, `
		SELECT identity.id::text, identity.server_id::text, identity.pending_activation
		FROM agent_identities AS identity
		JOIN servers ON servers.id=identity.server_id
		WHERE identity.credential_hash=$1 AND identity.revoked_at IS NULL
		  AND servers.enabled=true
		FOR UPDATE OF identity
	`, credentialHash).Scan(&identityID, &serverID, &pending)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAgentCredentialInvalid
	}
	if err != nil {
		return "", err
	}
	if pending {
		if _, err := tx.Exec(ctx, `
			UPDATE agent_identities SET revoked_at=$3
			WHERE server_id=$1 AND id<>$2 AND revoked_at IS NULL
		`, serverID, identityID, authenticatedAt); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agent_identities
		SET last_authenticated_at=$2, pending_activation=false
		WHERE id=$1
	`, identityID, authenticatedAt); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return serverID, nil
}

func (r *PostgresRepository) CreatePendingIdentity(ctx context.Context, identity PendingAgentIdentity) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM servers WHERE id=$1 AND enabled=true FOR UPDATE)
	`, identity.ServerID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrServerNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agent_identities SET revoked_at=$2
		WHERE server_id=$1 AND pending_activation=true AND revoked_at IS NULL
	`, identity.ServerID, identity.CreatedAt); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO agent_identities (
			id, server_id, credential_hash, pending_activation, rotated_from_id, created_at
		) VALUES (
			$1,$2,$3,true,
			(SELECT id FROM agent_identities WHERE server_id=$2 AND revoked_at IS NULL
			 AND pending_activation=false ORDER BY created_at DESC LIMIT 1),
			$4
		)
	`, identity.IdentityID, identity.ServerID, identity.CredentialHash, identity.CreatedAt)
	if err != nil {
		return fmt.Errorf("创建待激活代理身份：%w", err)
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepository) RevokeServerCredentials(ctx context.Context, serverID string, revokedAt time.Time) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `
		UPDATE servers SET status='offline', updated_at=$2 WHERE id=$1
	`, serverID, revokedAt)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrServerNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agent_identities SET revoked_at=$2
		WHERE server_id=$1 AND revoked_at IS NULL
	`, serverID, revokedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepository) Dashboard(ctx context.Context) (Dashboard, error) {
	dashboard := Dashboard{Servers: []ServerView{}, RecentEvents: []RecentEvent{}}
	if err := r.db.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE enabled = true AND status IN ('online', 'draining')),
			count(*)
		FROM servers
	`).Scan(&dashboard.OnlineServers, &dashboard.TotalServers); err != nil {
		return Dashboard{}, err
	}
	if err := r.db.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE state = 'running'),
			count(*) FILTER (WHERE state = 'queued'),
			COALESCE(
				round(
					100.0 * count(*) FILTER (
						WHERE state = 'succeeded' AND finished_at >= date_trunc('day', now())
					) / NULLIF(count(*) FILTER (
						WHERE state IN ('succeeded', 'failed', 'timed_out')
							AND finished_at >= date_trunc('day', now())
					), 0),
					1
				),
				100.0
			)
		FROM task_runs
	`).Scan(&dashboard.RunningRuns, &dashboard.QueuedRuns, &dashboard.TodaySuccessRate); err != nil {
		return Dashboard{}, err
	}
	servers, err := r.ListServers(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	dashboard.Servers = servers

	rows, err := r.db.Query(ctx, `
		SELECT id, event_type, COALESCE(NULLIF(payload->>'message', ''), '任务状态已更新'), occurred_at
		FROM run_events
		ORDER BY occurred_at DESC
		LIMIT 8
	`)
	if err != nil {
		return Dashboard{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var event RecentEvent
		if err := rows.Scan(&event.ID, &event.Type, &event.Message, &event.OccurredAt); err != nil {
			return Dashboard{}, err
		}
		dashboard.RecentEvents = append(dashboard.RecentEvents, event)
	}
	if err := rows.Err(); err != nil {
		return Dashboard{}, err
	}
	return dashboard, nil
}

func (r *PostgresRepository) ListServers(ctx context.Context) ([]ServerView, error) {
	rows, err := r.db.Query(ctx, serverViewSelect+` ORDER BY server.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	servers := make([]ServerView, 0)
	for rows.Next() {
		view, err := scanServerView(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, view)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return servers, nil
}

func (r *PostgresRepository) UpdateServer(
	ctx context.Context,
	id string,
	input UpdateServerInput,
) (ServerView, error) {
	if strings.TrimSpace(id) == "" || !validServerUpdate(input) {
		return ServerView{}, ErrInvalidServerUpdate
	}
	var name any
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	var labels any
	if input.Labels != nil {
		encoded, err := json.Marshal(*input.Labels)
		if err != nil {
			return ServerView{}, fmt.Errorf("编码服务器标签：%w", err)
		}
		labels = encoded
	}
	var weight any
	if input.SchedulingWeight != nil {
		weight = *input.SchedulingWeight
	}
	var enabled any
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	var draining any
	if input.Draining != nil {
		draining = *input.Draining
	}

	var updatedID string
	err := r.db.QueryRow(ctx, `
		UPDATE servers
		SET
			name = COALESCE($2::text, name),
			labels = COALESCE($3::jsonb, labels),
			scheduling_weight = COALESCE($4::integer, scheduling_weight),
			enabled = COALESCE($5::boolean, enabled),
			drain_requested = CASE
				WHEN COALESCE($5::boolean, enabled) = false THEN false
				ELSE COALESCE($6::boolean, drain_requested)
			END,
			status = CASE
				WHEN COALESCE($5::boolean, enabled) = false THEN 'offline'
				WHEN COALESCE($6::boolean, drain_requested) = true AND status = 'online' THEN 'draining'
				WHEN COALESCE($6::boolean, drain_requested) = false AND status = 'draining' THEN
					CASE
						WHEN last_seen_at >= now() - interval '15 seconds' THEN 'online'
						ELSE 'offline'
					END
				ELSE status
			END,
			updated_at = now()
		WHERE id = $1
		RETURNING id
	`, id, name, labels, weight, enabled, draining).Scan(&updatedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServerView{}, ErrServerNotFound
	}
	if err != nil {
		return ServerView{}, err
	}
	return r.serverByID(ctx, updatedID)
}

func (r *PostgresRepository) serverByID(ctx context.Context, id string) (ServerView, error) {
	view, err := scanServerView(r.db.QueryRow(ctx, serverViewSelect+` WHERE server.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ServerView{}, ErrServerNotFound
	}
	return view, err
}

const serverViewSelect = `
	SELECT
		server.id,
		server.name,
		server.cloud_provider,
		server.region,
		server.status,
		server.enabled,
		server.drain_requested,
		server.labels,
		server.runtimes,
		server.agent_version,
		server.scheduling_weight,
		COALESCE(snapshot.cpu_usage_percent, 0),
		COALESCE(snapshot.memory_total_bytes, 0),
		COALESCE(snapshot.memory_available_bytes, 0),
		COALESCE(snapshot.disk_total_bytes, 0),
		COALESCE(snapshot.disk_available_bytes, 0),
		COALESCE(snapshot.running_tasks, 0),
		server.last_seen_at
	FROM servers AS server
	LEFT JOIN LATERAL (
		SELECT
			cpu_usage_percent,
			memory_total_bytes,
			memory_available_bytes,
			disk_total_bytes,
			disk_available_bytes,
			running_tasks
		FROM server_snapshots
		WHERE server_id = server.id
		ORDER BY collected_at DESC
		LIMIT 1
	) AS snapshot ON true
`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanServerView(row rowScanner) (ServerView, error) {
	var view ServerView
	var labels []byte
	var runtimes []byte
	err := row.Scan(
		&view.ID,
		&view.Name,
		&view.CloudProvider,
		&view.Region,
		&view.Status,
		&view.Enabled,
		&view.Draining,
		&labels,
		&runtimes,
		&view.AgentVersion,
		&view.SchedulingWeight,
		&view.CPUUsagePercent,
		&view.MemoryTotalBytes,
		&view.MemoryAvailableBytes,
		&view.DiskTotalBytes,
		&view.DiskAvailableBytes,
		&view.RunningTasks,
		&view.LastSeenAt,
	)
	if err != nil {
		return ServerView{}, err
	}
	if err := json.Unmarshal(labels, &view.Labels); err != nil {
		return ServerView{}, fmt.Errorf("解析服务器标签：%w", err)
	}
	if err := json.Unmarshal(runtimes, &view.Runtimes); err != nil {
		return ServerView{}, fmt.Errorf("解析服务器运行环境：%w", err)
	}
	if view.Labels == nil {
		view.Labels = map[string]string{}
	}
	if view.Runtimes == nil {
		view.Runtimes = []string{}
	}
	return view, nil
}

var _ EnrollmentRepository = (*PostgresRepository)(nil)
var _ HeartbeatRepository = (*PostgresRepository)(nil)
var _ ManagementQuery = (*PostgresRepository)(nil)
