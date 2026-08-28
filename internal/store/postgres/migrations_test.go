package postgres

import "testing"

func TestInitialMigrationCreatesCoreTables(t *testing.T) {
	db := startPostgres(t)
	applyMigrations(t, db)

	for _, table := range []string{
		"servers",
		"script_versions",
		"task_runs",
		"resource_leases",
		"audit_logs",
	} {
		if !tableExists(t, db, table) {
			t.Errorf("初始迁移后应存在数据表 %q", table)
		}
	}
}
