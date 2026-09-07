package agentupgrade

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

func (r *PostgresRepository) ActivePlan(ctx context.Context) (*Plan, error) {
	plan, err := r.planByQuery(ctx, planSelect+` WHERE plan.status IN ('pending','running','paused') ORDER BY plan.created_at DESC LIMIT 1`)
	if errors.Is(err, ErrPlanNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

func (r *PostgresRepository) Release(ctx context.Context, id string) (ReleaseInfo, error) {
	return r.releaseBy(ctx, `release.id = $1`, id)
}

func (r *PostgresRepository) ReleaseByVersion(ctx context.Context, version string) (ReleaseInfo, error) {
	return r.releaseBy(ctx, `release.version = $1`, version)
}

func (r *PostgresRepository) releaseBy(ctx context.Context, predicate string, value string) (ReleaseInfo, error) {
	var release ReleaseInfo
	var capabilities []byte
	err := r.db.QueryRow(ctx, `SELECT release.id, release.version, release.status, release.capabilities FROM agent_releases AS release WHERE `+predicate, value).Scan(&release.ID, &release.Version, &release.Status, &capabilities)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReleaseInfo{}, ErrReleaseNotFound
	}
	if err != nil {
		return ReleaseInfo{}, err
	}
	if err := json.Unmarshal(capabilities, &release.Capabilities); err != nil {
		return ReleaseInfo{}, err
	}
	rows, err := r.db.Query(ctx, `SELECT os, arch FROM agent_release_artifacts WHERE release_id = $1 ORDER BY arch`, release.ID)
	if err != nil {
		return ReleaseInfo{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ArtifactInfo
		if err := rows.Scan(&item.OS, &item.Arch); err != nil {
			return ReleaseInfo{}, err
		}
		release.Artifacts = append(release.Artifacts, item)
	}
	return release, rows.Err()
}

func (r *PostgresRepository) Servers(ctx context.Context, ids []string) ([]ServerInfo, error) {
	servers := make([]ServerInfo, 0, len(ids))
	for _, id := range ids {
		var server ServerInfo
		var capabilities []byte
		err := r.db.QueryRow(ctx, `SELECT id, status, enabled, drain_requested, agent_version, agent_os, agent_arch, agent_capabilities FROM servers WHERE id = $1`, id).Scan(
			&server.ID, &server.Status, &server.Enabled, &server.Draining, &server.AgentVersion, &server.AgentOS, &server.AgentArch, &capabilities,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrServerIneligible
		}
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(capabilities, &server.Capabilities); err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func (r *PostgresRepository) CreatePlan(ctx context.Context, plan Plan) (Plan, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO agent_upgrade_plans (
			id, target_release_id, status, first_batch_size, batch_size, drain_timeout_seconds,
			reconnect_timeout_seconds, verification_seconds, current_batch, created_by,
			pause_reason, created_at, started_at, finished_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::uuid,$11,$12,$13,$14)
	`, plan.ID, plan.TargetReleaseID, plan.Status, plan.FirstBatchSize, plan.BatchSize, plan.DrainTimeoutSeconds,
		plan.ReconnectTimeoutSeconds, plan.VerificationSeconds, plan.CurrentBatch, plan.CreatedBy,
		plan.PauseReason, plan.CreatedAt, plan.StartedAt, plan.FinishedAt)
	if err != nil {
		return Plan{}, mapPostgresError(err)
	}
	for _, target := range plan.Targets {
		_, err = tx.Exec(ctx, `
			INSERT INTO agent_upgrade_targets (
				id, plan_id, server_id, batch_number, source_version, target_version, source_draining,
				status, attempts, command_id, error_code, error_message, started_at, updated_at, finished_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		`, target.ID, plan.ID, target.ServerID, target.BatchNumber, target.SourceVersion, target.TargetVersion,
			target.SourceDraining, target.Status, target.Attempts, target.CommandID, target.ErrorCode,
			target.ErrorMessage, target.StartedAt, target.UpdatedAt, target.FinishedAt)
		if err != nil {
			return Plan{}, mapPostgresError(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Plan{}, mapPostgresError(err)
	}
	return r.Plan(ctx, plan.ID)
}

func (r *PostgresRepository) ListPlans(ctx context.Context) ([]Plan, error) {
	rows, err := r.db.Query(ctx, planSelect+` ORDER BY plan.created_at DESC, plan.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := []Plan{}
	for rows.Next() {
		plan, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		plan.Targets, err = r.targets(ctx, plan.ID)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (r *PostgresRepository) Plan(ctx context.Context, id string) (Plan, error) {
	return r.planByQuery(ctx, planSelect+` WHERE plan.id = $1`, id)
}

func (r *PostgresRepository) SavePlan(ctx context.Context, plan Plan) (Plan, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists string
	if err := tx.QueryRow(ctx, `SELECT id FROM agent_upgrade_plans WHERE id = $1 FOR UPDATE`, plan.ID).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrPlanNotFound
	} else if err != nil {
		return Plan{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_upgrade_plans SET status=$2,current_batch=$3,pause_reason=$4,started_at=$5,finished_at=$6 WHERE id=$1`, plan.ID, plan.Status, plan.CurrentBatch, plan.PauseReason, plan.StartedAt, plan.FinishedAt)
	if err != nil {
		return Plan{}, mapPostgresError(err)
	}
	for _, target := range plan.Targets {
		result, err := tx.Exec(ctx, `UPDATE agent_upgrade_targets SET status=$3,attempts=$4,command_id=$5,error_code=$6,error_message=$7,started_at=$8,updated_at=$9,finished_at=$10 WHERE id=$1 AND plan_id=$2`, target.ID, plan.ID, target.Status, target.Attempts, target.CommandID, target.ErrorCode, target.ErrorMessage, target.StartedAt, target.UpdatedAt, target.FinishedAt)
		if err != nil {
			return Plan{}, err
		}
		if result.RowsAffected() != 1 {
			return Plan{}, ErrTargetNotFound
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Plan{}, mapPostgresError(err)
	}
	return r.Plan(ctx, plan.ID)
}

const planSelect = `SELECT plan.id, plan.target_release_id, release.version, plan.status, plan.first_batch_size, plan.batch_size, plan.drain_timeout_seconds, plan.reconnect_timeout_seconds, plan.verification_seconds, plan.current_batch, plan.created_by::text, plan.pause_reason, plan.created_at, plan.started_at, plan.finished_at FROM agent_upgrade_plans AS plan JOIN agent_releases AS release ON release.id = plan.target_release_id`

type scanner interface{ Scan(...any) error }

func scanPlan(row scanner) (Plan, error) {
	var plan Plan
	var started, finished sql.NullTime
	err := row.Scan(&plan.ID, &plan.TargetReleaseID, &plan.TargetVersion, &plan.Status, &plan.FirstBatchSize, &plan.BatchSize, &plan.DrainTimeoutSeconds, &plan.ReconnectTimeoutSeconds, &plan.VerificationSeconds, &plan.CurrentBatch, &plan.CreatedBy, &plan.PauseReason, &plan.CreatedAt, &started, &finished)
	if err != nil {
		return Plan{}, err
	}
	if started.Valid {
		plan.StartedAt = &started.Time
	}
	if finished.Valid {
		plan.FinishedAt = &finished.Time
	}
	return plan, nil
}
func (r *PostgresRepository) planByQuery(ctx context.Context, query string, args ...any) (Plan, error) {
	plan, err := scanPlan(r.db.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrPlanNotFound
	}
	if err != nil {
		return Plan{}, err
	}
	plan.Targets, err = r.targets(ctx, plan.ID)
	return plan, err
}
func (r *PostgresRepository) targets(ctx context.Context, planID string) ([]Target, error) {
	rows, err := r.db.Query(ctx, `SELECT id,plan_id,server_id,batch_number,source_version,target_version,source_draining,status,attempts,command_id,error_code,error_message,started_at,updated_at,finished_at FROM agent_upgrade_targets WHERE plan_id=$1 ORDER BY batch_number,updated_at,id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []Target{}
	for rows.Next() {
		var target Target
		var started, finished sql.NullTime
		if err := rows.Scan(&target.ID, &target.PlanID, &target.ServerID, &target.BatchNumber, &target.SourceVersion, &target.TargetVersion, &target.SourceDraining, &target.Status, &target.Attempts, &target.CommandID, &target.ErrorCode, &target.ErrorMessage, &started, &target.UpdatedAt, &finished); err != nil {
			return nil, err
		}
		if started.Valid {
			target.StartedAt = &started.Time
		}
		if finished.Valid {
			target.FinishedAt = &finished.Time
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}
func mapPostgresError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" && postgresError.ConstraintName == "agent_upgrade_plans_one_active_idx" {
		return ErrActivePlanExists
	}
	if err != nil {
		return fmt.Errorf("保存代理升级计划：%w", err)
	}
	return nil
}
