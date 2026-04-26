// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore is the cluster-aware Store backend. Functions and
// cron jobs persist in tables; mutations broadcast via LISTEN/NOTIFY
// to peer replicas.
type PostgresStore struct {
	pool   *pgxpool.Pool
	logger *slog.Logger

	subsMu sync.Mutex
	subs   map[chan Event]struct{}

	stopListen chan struct{}
	listenDone chan struct{}
}

// OpenPostgres connects, runs migrations, and starts the LISTEN loop.
func OpenPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("state/pg: parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("state/pg: connect: %w", err)
	}

	s := &PostgresStore{
		pool:       pool,
		logger:     slog.Default(),
		subs:       map[chan Event]struct{}{},
		stopListen: make(chan struct{}),
		listenDone: make(chan struct{}),
	}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	go s.listen()
	return s, nil
}

// Close stops the LISTEN loop and releases the connection pool.
func (s *PostgresStore) Close() error {
	close(s.stopListen)
	<-s.listenDone

	s.subsMu.Lock()
	for ch := range s.subs {
		close(ch)
	}
	s.subs = nil
	s.subsMu.Unlock()

	s.pool.Close()
	return nil
}

// migrate applies any embedded SQL files in lexical order that haven't
// run yet, tracked by version in cfunc_schema.
func (s *PostgresStore) migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS cfunc_schema (
			version INT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("state/pg: ensure schema table: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("state/pg: read migrations: %w", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		// Filename: "0001_init.sql" → version 1
		var version int
		if _, err := fmt.Sscanf(e.Name(), "%d_", &version); err != nil {
			return fmt.Errorf("state/pg: parse migration name %s: %w", e.Name(), err)
		}

		var applied bool
		if err := s.pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM cfunc_schema WHERE version=$1)",
			version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("state/pg: migration %s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO cfunc_schema (version) VALUES ($1)", version); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		s.logger.Info("state/pg: migration applied", "version", version, "file", e.Name())
	}
	return nil
}

// --- Function CRUD ---------------------------------------------------

func (s *PostgresStore) GetFunction(ctx context.Context, name string) (FunctionDef, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT name, project, bin_path, env, layers, max_concurrency, updated_at
		FROM cfunc_functions WHERE name = $1`, name)
	return scanFunction(row)
}

func (s *PostgresStore) ListFunctions(ctx context.Context) ([]FunctionDef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, project, bin_path, env, layers, max_concurrency, updated_at
		FROM cfunc_functions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FunctionDef{}
	for rows.Next() {
		d, err := scanFunction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) PutFunction(ctx context.Context, d FunctionDef) error {
	if d.Project == "" {
		d.Project = "default"
	}
	envJSON, _ := json.Marshal(orEmpty(d.Env))
	layersJSON, _ := json.Marshal(orEmptyLayers(d.Layers))
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cfunc_functions (name, project, bin_path, env, layers, max_concurrency)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (name) DO UPDATE SET
			project = EXCLUDED.project,
			bin_path = EXCLUDED.bin_path,
			env = EXCLUDED.env,
			layers = EXCLUDED.layers,
			max_concurrency = EXCLUDED.max_concurrency,
			updated_at = now()`,
		d.Name, d.Project, d.Binary, envJSON, layersJSON, d.MaxConcurrency)
	return err
}

func (s *PostgresStore) DeleteFunction(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM cfunc_functions WHERE name = $1", name)
	return err
}

func scanFunction(row pgx.Row) (FunctionDef, error) {
	var d FunctionDef
	var envJSON, layersJSON []byte
	if err := row.Scan(&d.Name, &d.Project, &d.Binary, &envJSON, &layersJSON,
		&d.MaxConcurrency, &d.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return FunctionDef{}, ErrNotFound
		}
		return FunctionDef{}, err
	}
	if len(envJSON) > 0 {
		_ = json.Unmarshal(envJSON, &d.Env)
	}
	if len(layersJSON) > 0 {
		_ = json.Unmarshal(layersJSON, &d.Layers)
	}
	return d, nil
}

// --- Cron CRUD -------------------------------------------------------

func (s *PostgresStore) GetCronJob(ctx context.Context, id string) (CronJob, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, project, schedule, function, method, body, headers, updated_at
		FROM cfunc_cron_jobs WHERE id = $1`, id)
	return scanCron(row)
}

func (s *PostgresStore) ListCronJobs(ctx context.Context) ([]CronJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project, schedule, function, method, body, headers, updated_at
		FROM cfunc_cron_jobs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CronJob{}
	for rows.Next() {
		j, err := scanCron(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *PostgresStore) PutCronJob(ctx context.Context, j CronJob) error {
	if j.Project == "" {
		j.Project = "default"
	}
	headersJSON, _ := json.Marshal(j.Headers)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cfunc_cron_jobs (id, project, schedule, function, method, body, headers)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
			project = EXCLUDED.project,
			schedule = EXCLUDED.schedule,
			function = EXCLUDED.function,
			method = EXCLUDED.method,
			body = EXCLUDED.body,
			headers = EXCLUDED.headers,
			updated_at = now()`,
		j.ID, j.Project, j.Schedule, j.Function, j.Method, j.Body, headersJSON)
	return err
}

func (s *PostgresStore) DeleteCronJob(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM cfunc_cron_jobs WHERE id = $1", id)
	return err
}

func scanCron(row pgx.Row) (CronJob, error) {
	var j CronJob
	var headersJSON []byte
	var method, body *string
	if err := row.Scan(&j.ID, &j.Project, &j.Schedule, &j.Function,
		&method, &body, &headersJSON, &j.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CronJob{}, ErrNotFound
		}
		return CronJob{}, err
	}
	if method != nil {
		j.Method = *method
	}
	if body != nil {
		j.Body = *body
	}
	if len(headersJSON) > 0 {
		_ = json.Unmarshal(headersJSON, &j.Headers)
	}
	return j, nil
}

// --- Watch -----------------------------------------------------------

func (s *PostgresStore) Watch(ctx context.Context) (<-chan Event, error) {
	ch := make(chan Event, 16)
	s.subsMu.Lock()
	if s.subs == nil {
		s.subsMu.Unlock()
		return nil, errors.New("state/pg: store closed")
	}
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()

	go func() {
		<-ctx.Done()
		s.subsMu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.subsMu.Unlock()
	}()
	return ch, nil
}

// listen owns a dedicated connection that LISTENs on cfunc_state and
// fans incoming notifications out to all current subscribers. Pool
// connections can't LISTEN (they get returned to the pool); we keep
// this one outside the pool entirely.
func (s *PostgresStore) listen() {
	defer close(s.listenDone)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		select {
		case <-s.stopListen:
			return
		default:
		}

		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			s.logger.Warn("state/pg: listen acquire", "err", err)
			time.Sleep(time.Second)
			continue
		}
		// Reserve the connection from the pool: hijack it for the
		// duration of the LISTEN loop so the pool doesn't recycle it.
		pgConn := conn.Hijack()

		if err := s.runListen(ctx, pgConn); err != nil &&
			!errors.Is(err, context.Canceled) {
			s.logger.Warn("state/pg: listen loop", "err", err)
		}

		pgConn.Close(context.Background())

		// Brief backoff before retrying — connection may have died.
		select {
		case <-s.stopListen:
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *PostgresStore) runListen(ctx context.Context, conn *pgx.Conn) error {
	for _, ch := range []string{"cfunc_functions", "cfunc_crons"} {
		if _, err := conn.Exec(ctx, "LISTEN "+ch); err != nil {
			return err
		}
	}
	for {
		select {
		case <-s.stopListen:
			return nil
		default:
		}

		// WaitForNotification blocks until a notification arrives or
		// the context is cancelled. Use a short timeout so stopListen
		// can break us out.
		nctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		n, err := conn.WaitForNotification(nctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue // tick: re-check stopListen
			}
			if pgconn.Timeout(err) {
				continue
			}
			return err
		}
		s.dispatch(n)
	}
}

func (s *PostgresStore) dispatch(n *pgconn.Notification) {
	// Payload format: "<op>:<name>"  e.g. "put:hello", "delete:cron-1"
	op, name, ok := strings.Cut(n.Payload, ":")
	if !ok {
		return
	}
	var kind EventKind
	switch n.Channel {
	case "cfunc_functions":
		switch op {
		case "put":
			kind = EventFunctionPut
		case "delete":
			kind = EventFunctionDelete
		default:
			return
		}
	case "cfunc_crons":
		switch op {
		case "put":
			kind = EventCronPut
		case "delete":
			kind = EventCronDelete
		default:
			return
		}
	default:
		return
	}
	ev := Event{Kind: kind, Name: name}
	s.subsMu.Lock()
	for ch := range s.subs {
		select {
		case ch <- ev:
		default: // drop on slow subscriber
		}
	}
	s.subsMu.Unlock()
}

// --- Leader election --------------------------------------------------

// TryAcquireLeadership attempts a session-scoped advisory lock keyed on
// hash(key). Returns a release func on success; non-nil error otherwise.
// The lock is automatically released when the underlying connection
// returns to the pool — in our case, only when release() is invoked.
func (s *PostgresStore) TryAcquireLeadership(ctx context.Context, key string) (func(), error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	id := keyToInt64(key)
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", id).Scan(&ok); err != nil {
		conn.Release()
		return nil, err
	}
	if !ok {
		conn.Release()
		return nil, errors.New("state/pg: leadership held by another process")
	}
	return func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", id)
		conn.Release()
	}, nil
}

func keyToInt64(s string) int64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return int64(h.Sum64())
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyLayers(l []LayerMount) []LayerMount {
	if l == nil {
		return []LayerMount{}
	}
	return l
}
