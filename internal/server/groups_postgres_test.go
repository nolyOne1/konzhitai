package server_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"yunling.local/platform/internal/server"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresServerGroupMembershipAndLegacyMigration(t *testing.T) {
	db := testpostgres.Start(t)
	files, err := filepath.Glob(filepath.Join(testpostgres.RepositoryRoot(t), "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if filepath.Base(file) < "000016" {
			testpostgres.ApplyMigration(t, db, filepath.Base(file))
		}
	}
	ctx := context.Background()
	id := "123e4567-e89b-42d3-a456-426614174299"
	if _, err := db.Exec(ctx, `INSERT INTO servers(id,name,server_group_id) VALUES($1,'节点','legacy-group')`, id); err != nil {
		t.Fatal(err)
	}
	testpostgres.ApplyMigration(t, db, "000016_server_groups.up.sql")
	repository := server.NewPostgresRepository(db)
	groups, err := repository.ListGroups(ctx)
	if err != nil || len(groups) != 1 || groups[0].ID != "legacy-group" || groups[0].ServerCount != 1 {
		t.Fatalf("legacy membership lost: %+v %v", groups, err)
	}
	group, err := repository.CreateGroup(ctx, " 生产组 ")
	if err != nil {
		t.Fatal(err)
	}
	if group.Name != "生产组" || group.ID == "" {
		t.Fatalf("invalid group: %+v", group)
	}
	if _, err := repository.CreateGroup(ctx, "生产组"); !errors.Is(err, server.ErrGroupNameExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := repository.CreateGroup(ctx, strings.Repeat("长", 81)); !errors.Is(err, server.ErrInvalidGroup) {
		t.Fatalf("long name: %v", err)
	}
	updated, err := repository.UpdateServer(ctx, id, server.UpdateServerInput{ServerGroupID: &group.ID})
	if err != nil || updated.ServerGroupID != group.ID {
		t.Fatalf("assignment: %+v %v", updated, err)
	}
	renamed, err := repository.RenameGroup(ctx, group.ID, "夜间批处理")
	if err != nil || renamed.ID != group.ID || renamed.ServerCount != 1 {
		t.Fatalf("rename must keep stable id and count: %+v %v", renamed, err)
	}
	unknown := "missing-group"
	if _, err := repository.UpdateServer(ctx, id, server.UpdateServerInput{ServerGroupID: &unknown}); !errors.Is(err, server.ErrGroupNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	empty := ""
	updated, err = repository.UpdateServer(ctx, id, server.UpdateServerInput{ServerGroupID: &empty})
	if err != nil || updated.ServerGroupID != "" {
		t.Fatalf("remove membership: %+v %v", updated, err)
	}
}
