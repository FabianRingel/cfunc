// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// pgDSN returns the test DSN, or skips the test if not configured.
// CI / dev sets TEST_PG_DSN to enable Postgres tests.
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set; skipping Postgres tests")
	}
	return dsn
}

// freshPGStore opens a PostgresStore against the test DSN, runs
// migrations, and truncates state tables so each test starts clean.
func freshPGStore(t *testing.T) *PostgresStore {
	t.Helper()
	s, err := OpenPostgres(context.Background(), pgDSN(t))
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.pool.Exec(context.Background(),
		`TRUNCATE cfunc_functions, cfunc_cron_jobs,
		         cfunc_api_keys, cfunc_quotas, cfunc_quota_usage, cfunc_audit_log`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// Truncate non-default projects so multi-project tests start clean
	// without breaking the FK-protected default seed row.
	if _, err := s.pool.Exec(context.Background(),
		`DELETE FROM cfunc_projects WHERE name != 'default'`); err != nil {
		t.Fatalf("clean projects: %v", err)
	}
	return s
}

func TestPGPutGetFunction(t *testing.T) {
	s := freshPGStore(t)
	ctx := context.Background()
	if err := s.PutFunction(ctx, FunctionDef{
		Name: "hello", Binary: "/x", MaxConcurrency: 4,
		Env: []string{"K=V"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFunction(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "hello" || got.Binary != "/x" {
		t.Fatalf("got %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt not set")
	}
	if got.Project != "default" {
		t.Fatalf("project=%q", got.Project)
	}
	if len(got.Env) != 1 || got.Env[0] != "K=V" {
		t.Fatalf("env=%v", got.Env)
	}
}

func TestPGGetMissing(t *testing.T) {
	s := freshPGStore(t)
	_, err := s.GetFunction(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v want ErrNotFound", err)
	}
}

func TestPGListAndDelete(t *testing.T) {
	s := freshPGStore(t)
	ctx := context.Background()
	for _, n := range []string{"a", "b", "c"} {
		_ = s.PutFunction(ctx, FunctionDef{Name: n, Binary: "/" + n})
	}
	list, _ := s.ListFunctions(ctx)
	if len(list) != 3 {
		t.Fatalf("got %d", len(list))
	}
	if err := s.DeleteFunction(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListFunctions(ctx)
	if len(list) != 2 {
		t.Fatalf("got %d after delete", len(list))
	}
}

func TestPGPutReplaces(t *testing.T) {
	s := freshPGStore(t)
	ctx := context.Background()
	_ = s.PutFunction(ctx, FunctionDef{Name: "x", Binary: "/old"})
	_ = s.PutFunction(ctx, FunctionDef{Name: "x", Binary: "/new"})
	got, _ := s.GetFunction(ctx, "x")
	if got.Binary != "/new" {
		t.Fatalf("binary=%q", got.Binary)
	}
}

// TestPGWatchCrossInstance is the headline cluster-mode test:
// a Put on store-A is delivered to a Watcher on store-B via Postgres
// LISTEN/NOTIFY. Two stores = two pgx pools = simulates two replicas.
func TestPGWatchCrossInstance(t *testing.T) {
	a := freshPGStore(t)
	b, err := OpenPostgres(context.Background(), pgDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := b.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Give the LISTEN goroutine a moment to subscribe before we fire.
	time.Sleep(100 * time.Millisecond)

	if err := a.PutFunction(ctx, FunctionDef{Name: "cross", Binary: "/x"}); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Kind != EventFunctionPut || ev.Name != "cross" {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cross-instance NOTIFY not delivered within 3s")
	}
}

func TestPGWatchDeletes(t *testing.T) {
	s := freshPGStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = s.PutFunction(ctx, FunctionDef{Name: "x", Binary: "/x"})

	ch, _ := s.Watch(ctx)
	time.Sleep(100 * time.Millisecond)

	_ = s.DeleteFunction(ctx, "x")

	// The put NOTIFY may still be in flight when we subscribed; drain
	// non-matching events until we see the delete or time out.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == EventFunctionDelete && ev.Name == "x" {
				return
			}
		case <-deadline:
			t.Fatal("delete NOTIFY not delivered")
		}
	}
}

func TestPGCronCRUD(t *testing.T) {
	s := freshPGStore(t)
	ctx := context.Background()
	if err := s.PutCronJob(ctx, CronJob{
		ID: "daily", Schedule: "0 9 * * *", Function: "report",
		Headers: map[string]string{"X-Tag": "prod"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCronJob(ctx, "daily")
	if err != nil {
		t.Fatal(err)
	}
	if got.Function != "report" || got.Headers["X-Tag"] != "prod" {
		t.Fatalf("got %+v", got)
	}
	if err := s.DeleteCronJob(ctx, "daily"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCronJob(ctx, "daily"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestPGAdvisoryLock proves pg_try_advisory_lock works for cluster
// leader-election: only one of two callers can hold it at a time.
func TestPGAdvisoryLock(t *testing.T) {
	a := freshPGStore(t)
	b, err := OpenPostgres(context.Background(), pgDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	ctx := context.Background()
	const key = "test-leader"

	releaseA, err := a.TryAcquireLeadership(ctx, key)
	if err != nil {
		t.Fatalf("a should acquire: %v", err)
	}

	if _, err := b.TryAcquireLeadership(ctx, key); err == nil {
		t.Fatal("b should NOT acquire while a holds the lock")
	}

	releaseA()

	releaseB, err := b.TryAcquireLeadership(ctx, key)
	if err != nil {
		t.Fatalf("b should acquire after a released: %v", err)
	}
	releaseB()
}
