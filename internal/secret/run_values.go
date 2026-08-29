package secret

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrRunAccessDenied = errors.New("运行实例或执行令牌无效")

type runSecretResolver interface {
	ResolveForRun(context.Context, []ID) (map[string]string, error)
}

type RunValueSource struct {
	db       *pgxpool.Pool
	resolver runSecretResolver
}

func NewRunValueSource(db *pgxpool.Pool, resolver runSecretResolver) *RunValueSource {
	return &RunValueSource{db: db, resolver: resolver}
}

func (s *RunValueSource) ValuesForRun(ctx context.Context, runID, executionToken string) ([][]byte, error) {
	if s == nil || s.db == nil || s.resolver == nil || runID == "" || executionToken == "" {
		return nil, ErrRunAccessDenied
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `
		SELECT definition.secret_bindings
		FROM task_runs AS run
		JOIN task_definitions AS definition ON definition.id=run.task_definition_id
		WHERE run.id=$1 AND run.execution_token=$2
	`, runID, executionToken).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRunAccessDenied
	}
	if err != nil {
		return nil, err
	}
	bindings := map[string]string{}
	if err := json.Unmarshal(raw, &bindings); err != nil {
		return nil, ErrInvalidSecret
	}
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	ids := make([]ID, 0, len(names))
	seen := make(map[ID]bool, len(names))
	for _, name := range names {
		id := ID(bindings[name])
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	resolved, err := s.resolver.ResolveForRun(ctx, ids)
	if err != nil {
		return nil, err
	}
	values := make([][]byte, 0, len(ids))
	for _, id := range ids {
		value, exists := resolved[string(id)]
		if !exists {
			return nil, ErrSecretNotFound
		}
		values = append(values, []byte(value))
	}
	return values, nil
}
