package secret

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ db *pgxpool.Pool }

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) Create(ctx context.Context, value StoredSecret) (Metadata, error) {
	if r == nil || r.db == nil {
		return Metadata{}, ErrInvalidSecret
	}
	var createdBy any
	if value.CreatedBy != "" {
		createdBy = value.CreatedBy
	}
	var metadata Metadata
	err := r.db.QueryRow(ctx, `
		INSERT INTO secrets (
			id, name, ciphertext, nonce, encrypted_data_key, data_key_nonce,
			key_version, scope, created_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id::text, name, scope, COALESCE(created_by::text,''), created_at, updated_at
	`, value.ID, value.Name, value.Ciphertext, value.CiphertextNonce,
		value.EncryptedDataKey, value.DataKeyNonce, value.KeyVersion, value.Scope, createdBy,
		value.CreatedAt, value.UpdatedAt,
	).Scan(&metadata.ID, &metadata.Name, &metadata.Scope, &metadata.CreatedBy, &metadata.CreatedAt, &metadata.UpdatedAt)
	if err != nil {
		return Metadata{}, fmt.Errorf("保存敏感参数：%w", err)
	}
	return metadata, nil
}

func (r *PostgresRepository) Load(ctx context.Context, ids []ID) ([]StoredSecret, error) {
	if r == nil || r.db == nil {
		return nil, ErrInvalidSecret
	}
	if len(ids) == 0 {
		return []StoredSecret{}, nil
	}
	values := make([]string, len(ids))
	for index, id := range ids {
		values[index] = string(id)
	}
	rows, err := r.db.Query(ctx, `
		SELECT id::text, name, scope, COALESCE(created_by::text,''), created_at, updated_at,
		       ciphertext, nonce, encrypted_data_key, data_key_nonce, key_version
		FROM secrets WHERE id = ANY($1::uuid[])
	`, values)
	if err != nil {
		return nil, fmt.Errorf("读取敏感参数密文：%w", err)
	}
	defer rows.Close()
	items := make([]StoredSecret, 0, len(ids))
	for rows.Next() {
		var item StoredSecret
		if err := rows.Scan(
			&item.ID, &item.Name, &item.Scope, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt,
			&item.Ciphertext, &item.CiphertextNonce, &item.EncryptedDataKey,
			&item.DataKeyNonce, &item.KeyVersion,
		); err != nil {
			return nil, fmt.Errorf("解析敏感参数密文：%w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历敏感参数密文：%w", err)
	}
	return items, nil
}

func (r *PostgresRepository) List(ctx context.Context) ([]Metadata, error) {
	if r == nil || r.db == nil {
		return nil, ErrInvalidSecret
	}
	rows, err := r.db.Query(ctx, `
		SELECT id::text, name, scope, COALESCE(created_by::text,''), created_at, updated_at
		FROM secrets WHERE scope='user' ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("读取敏感参数列表：%w", err)
	}
	defer rows.Close()
	items := []Metadata{}
	for rows.Next() {
		var item Metadata
		if err := rows.Scan(&item.ID, &item.Name, &item.Scope, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("解析敏感参数元数据：%w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历敏感参数列表：%w", err)
	}
	return items, nil
}

var _ Repository = (*PostgresRepository)(nil)
