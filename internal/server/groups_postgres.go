package server

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (r *PostgresRepository) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := r.db.Query(ctx, `SELECT g.id,g.name,count(s.id),g.created_at FROM server_groups g
		LEFT JOIN servers s ON s.server_group_id=g.id GROUP BY g.id ORDER BY g.name,g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		var group Group
		if err := rows.Scan(&group.ID, &group.Name, &group.ServerCount, &group.CreatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (r *PostgresRepository) CreateGroup(ctx context.Context, name string) (Group, error) {
	name, err := normalizedGroupName(name)
	if err != nil {
		return Group{}, err
	}
	var group Group
	err = r.db.QueryRow(ctx, `INSERT INTO server_groups(id,name) VALUES($1,$2) RETURNING id,name,created_at`, uuid.NewString(), name).
		Scan(&group.ID, &group.Name, &group.CreatedAt)
	return group, groupWriteError(err)
}

func (r *PostgresRepository) RenameGroup(ctx context.Context, id, name string) (Group, error) {
	name, err := normalizedGroupName(name)
	if err != nil {
		return Group{}, err
	}
	var group Group
	err = r.db.QueryRow(ctx, `UPDATE server_groups SET name=$2 WHERE id=$1 RETURNING id,name,
		(SELECT count(*) FROM servers WHERE server_group_id=$1),created_at`, id, name).
		Scan(&group.ID, &group.Name, &group.ServerCount, &group.CreatedAt)
	return group, groupWriteError(err)
}

func (r *PostgresRepository) validateGroup(ctx context.Context, groupID string) error {
	if groupID == "" {
		return nil
	}
	if strings.TrimSpace(groupID) != groupID {
		return ErrGroupNotFound
	}
	var exists bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM server_groups WHERE id=$1)`, groupID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrGroupNotFound
	}
	return nil
}

func groupWriteError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGroupNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return ErrGroupNameExists
	}
	return err
}
