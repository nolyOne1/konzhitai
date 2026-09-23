package server

import "context"

// dashboardActivity reports current work and only each script's latest published
// version, so healthy historical copies cannot hide a failed new deployment.
func (r *PostgresRepository) dashboardActivity(ctx context.Context, dashboard *Dashboard) error {
	dashboard.ActiveRuns = []DashboardRun{}
	rows, err := r.db.Query(ctx, `
		SELECT run.id, task.name, script.name, COALESCE(server.name, ''),
		       run.state, run.result_summary, run.queued_at
		FROM task_runs run
		JOIN task_definitions task ON task.id=run.task_definition_id
		JOIN scripts script ON script.id=task.script_id
		LEFT JOIN servers server ON server.id=run.assigned_server_id
		WHERE run.state IN ('queued','scheduling','assigned','syncing','running','unknown')
		ORDER BY CASE WHEN run.state='unknown' THEN 0 WHEN run.state='running' THEN 1 ELSE 2 END,
		         run.queued_at, run.id
		LIMIT 6
	`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var run DashboardRun
		if err := rows.Scan(&run.ID, &run.TaskName, &run.ScriptName, &run.ServerName, &run.State, &run.ResultSummary, &run.QueuedAt); err != nil {
			rows.Close()
			return err
		}
		dashboard.ActiveRuns = append(dashboard.ActiveRuns, run)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return r.db.QueryRow(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (script_id) id, script_id FROM script_versions
			ORDER BY script_id, version DESC
		)
		SELECT (SELECT count(*) FROM latest), count(sync.id),
		       count(sync.id) FILTER (WHERE sync.status='ready'),
		       count(sync.id) FILTER (WHERE sync.status='pending'),
		       count(sync.id) FILTER (WHERE sync.status='downloading'),
		       count(sync.id) FILTER (WHERE sync.status='failed'),
		       count(sync.id) FILTER (WHERE sync.status='drifted')
		FROM latest LEFT JOIN script_syncs sync ON sync.script_version_id=latest.id
	`).Scan(&dashboard.ScriptSync.PublishedScripts, &dashboard.ScriptSync.Total,
		&dashboard.ScriptSync.Ready, &dashboard.ScriptSync.Pending, &dashboard.ScriptSync.Downloading,
		&dashboard.ScriptSync.Failed, &dashboard.ScriptSync.Drifted)
}
