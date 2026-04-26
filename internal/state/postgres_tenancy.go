// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// --- Projects --------------------------------------------------------

func (s *PostgresStore) CreateProject(ctx context.Context, p Project) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cfunc_projects (name, description) VALUES ($1, $2)`,
		p.Name, p.Description)
	if err != nil {
		return fmt.Errorf("state/pg: create project: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetProject(ctx context.Context, name string) (Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT name, description, created_at FROM cfunc_projects WHERE name = $1`,
		name).Scan(&p.Name, &p.Description, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

func (s *PostgresStore) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, description, created_at FROM cfunc_projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Name, &p.Description, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteProject(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM cfunc_projects WHERE name = $1`, name)
	return err
}

// --- API Keys --------------------------------------------------------

func (s *PostgresStore) CreateAPIKey(ctx context.Context, k APIKey) error {
	if k.Project == "*" {
		return errors.New(`state/pg: api key project "*" is reserved for cluster-admin identity`)
	}
	scopesJSON, _ := json.Marshal(orEmpty(k.Scopes))
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cfunc_api_keys (id, project, description, token_sha256, scopes)
		VALUES ($1, $2, $3, $4, $5)`,
		k.ID, k.Project, k.Description, k.TokenSHA256, scopesJSON)
	if err != nil {
		return fmt.Errorf("state/pg: create api key: %w", err)
	}
	return nil
}

func (s *PostgresStore) LookupAPIKey(ctx context.Context, tokenSHA256 []byte) (APIKey, error) {
	var k APIKey
	var scopesJSON []byte
	var lastUsed *time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE cfunc_api_keys SET last_used_at = now()
		WHERE token_sha256 = $1
		RETURNING id, project, description, token_sha256, scopes, created_at, last_used_at`,
		tokenSHA256).Scan(&k.ID, &k.Project, &k.Description, &k.TokenSHA256,
		&scopesJSON, &k.CreatedAt, &lastUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	if len(scopesJSON) > 0 {
		_ = json.Unmarshal(scopesJSON, &k.Scopes)
	}
	k.LastUsedAt = lastUsed
	return k, nil
}

func (s *PostgresStore) ListAPIKeys(ctx context.Context, project string) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project, description, token_sha256, scopes, created_at, last_used_at
		FROM cfunc_api_keys WHERE project = $1 ORDER BY id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		var scopesJSON []byte
		var lastUsed *time.Time
		if err := rows.Scan(&k.ID, &k.Project, &k.Description, &k.TokenSHA256,
			&scopesJSON, &k.CreatedAt, &lastUsed); err != nil {
			return nil, err
		}
		if len(scopesJSON) > 0 {
			_ = json.Unmarshal(scopesJSON, &k.Scopes)
		}
		k.LastUsedAt = lastUsed
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteAPIKey(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM cfunc_api_keys WHERE id = $1`, id)
	return err
}

// --- Quotas ----------------------------------------------------------

func (s *PostgresStore) SetQuota(ctx context.Context, project, kind string, value int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cfunc_quotas (project, kind, value) VALUES ($1, $2, $3)
		ON CONFLICT (project, kind) DO UPDATE SET
			value = EXCLUDED.value,
			updated_at = now()`,
		project, kind, value)
	return err
}

func (s *PostgresStore) GetQuota(ctx context.Context, project, kind string) (int64, error) {
	var v int64
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM cfunc_quotas WHERE project = $1 AND kind = $2`,
		project, kind).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil // unset → unlimited, no error
	}
	return v, err
}

func (s *PostgresStore) ListQuotas(ctx context.Context, project string) ([]Quota, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT project, kind, value, updated_at FROM cfunc_quotas
		WHERE project = $1 ORDER BY kind`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Quota{}
	for rows.Next() {
		var q Quota
		if err := rows.Scan(&q.Project, &q.Kind, &q.Value, &q.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *PostgresStore) AddQuotaUsage(ctx context.Context, project, kind string, bucket time.Time, delta int64) error {
	if delta < 0 {
		return errors.New("state/pg: AddQuotaUsage delta must be non-negative")
	}
	bucket = bucket.UTC().Truncate(time.Minute)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cfunc_quota_usage (project, kind, bucket, value)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (project, kind, bucket) DO UPDATE SET
			value = cfunc_quota_usage.value + EXCLUDED.value`,
		project, kind, bucket, delta)
	return err
}

func (s *PostgresStore) GetQuotaUsage(ctx context.Context, project, kind string, since time.Time) (int64, error) {
	since = since.UTC().Truncate(time.Minute)
	var total *int64
	err := s.pool.QueryRow(ctx, `
		SELECT SUM(value) FROM cfunc_quota_usage
		WHERE project = $1 AND kind = $2 AND bucket >= $3`,
		project, kind, since).Scan(&total)
	if err != nil {
		return 0, err
	}
	if total == nil {
		return 0, nil
	}
	return *total, nil
}

// --- Audit log -------------------------------------------------------

func (s *PostgresStore) AppendAudit(ctx context.Context, e AuditEntry) error {
	metaJSON, _ := json.Marshal(orEmptyMap(e.Metadata))
	var project *string
	if e.Project != "" {
		project = &e.Project
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cfunc_audit_log (project, actor, action, target, metadata)
		VALUES ($1, $2, $3, $4, $5)`,
		project, e.Actor, e.Action, e.Target, metaJSON)
	return err
}

func (s *PostgresStore) ListAudit(ctx context.Context, project string, since time.Time, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	var (
		rows pgx.Rows
		err  error
	)
	if project == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT id, ts, project, actor, action, target, metadata
			FROM cfunc_audit_log
			WHERE project IS NULL AND ts >= $1
			ORDER BY ts DESC LIMIT $2`, sinceOrEpoch(since), limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT id, ts, project, actor, action, target, metadata
			FROM cfunc_audit_log
			WHERE project = $1 AND ts >= $2
			ORDER BY ts DESC LIMIT $3`, project, sinceOrEpoch(since), limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var proj *string
		var metaJSON []byte
		if err := rows.Scan(&e.ID, &e.TS, &proj, &e.Actor, &e.Action, &e.Target, &metaJSON); err != nil {
			return nil, err
		}
		if proj != nil {
			e.Project = *proj
		}
		if len(metaJSON) > 0 {
			_ = json.Unmarshal(metaJSON, &e.Metadata)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func sinceOrEpoch(t time.Time) time.Time {
	if t.IsZero() {
		return time.Unix(0, 0)
	}
	return t
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
