// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

// Postgres smoke tests for tenancy. The InMemStore tests in
// tenancy_test.go cover edge cases; here we just confirm the SQL
// works against a real Postgres.

func TestPGProjectAPIKeyQuotaAudit(t *testing.T) {
	s := freshPGStore(t)
	ctx := context.Background()

	if err := s.CreateProject(ctx, Project{Name: "acme", Description: "smoke"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(ctx, Project{Name: "acme"}); err == nil {
		t.Fatal("expected duplicate project to fail")
	}
	p, err := s.GetProject(ctx, "acme")
	if err != nil || p.Name != "acme" {
		t.Fatalf("get: %+v %v", p, err)
	}

	// API key.
	tok := sha256.Sum256([]byte("secret"))
	key := APIKey{
		ID: "ck_pg", Project: "acme", Description: "ci",
		TokenSHA256: tok[:], Scopes: []string{"deploy", "invoke"},
	}
	if err := s.CreateAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	got, err := s.LookupAPIKey(ctx, tok[:])
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "ck_pg" || got.Project != "acme" {
		t.Fatalf("lookup: %+v", got)
	}
	if got.LastUsedAt == nil {
		t.Fatal("LastUsedAt should be set after lookup")
	}
	if len(got.Scopes) != 2 {
		t.Fatalf("scopes: %v", got.Scopes)
	}

	other := sha256.Sum256([]byte("nope"))
	if _, err := s.LookupAPIKey(ctx, other[:]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown key: %v", err)
	}

	// Quotas + usage.
	if err := s.SetQuota(ctx, "acme", "requests_per_min", 100); err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuota(ctx, "acme", "requests_per_min", 200); err != nil {
		t.Fatal(err) // upsert
	}
	v, _ := s.GetQuota(ctx, "acme", "requests_per_min")
	if v != 200 {
		t.Fatalf("got %d, want 200", v)
	}
	v, _ = s.GetQuota(ctx, "acme", "missing")
	if v != 0 {
		t.Fatalf("missing quota: got %d, want 0", v)
	}

	now := time.Now().UTC().Truncate(time.Minute)
	for _, d := range []int64{3, 4, 5} {
		if err := s.AddQuotaUsage(ctx, "acme", "requests_per_min", now, d); err != nil {
			t.Fatal(err)
		}
	}
	used, err := s.GetQuotaUsage(ctx, "acme", "requests_per_min", now)
	if err != nil {
		t.Fatal(err)
	}
	if used != 12 {
		t.Fatalf("usage got %d, want 12", used)
	}

	// Audit log.
	for _, action := range []string{"function.put", "key.create"} {
		if err := s.AppendAudit(ctx, AuditEntry{
			Project: "acme", Actor: "ck_pg", Action: action, Target: "x",
		}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := s.ListAudit(ctx, "acme", time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit got %d", len(entries))
	}
	if entries[0].Action != "key.create" {
		t.Fatalf("expected newest first, got %s", entries[0].Action)
	}

	// Cluster-level audit.
	if err := s.AppendAudit(ctx, AuditEntry{
		Actor: "admin", Action: "project.create", Target: "acme",
	}); err != nil {
		t.Fatal(err)
	}
	cluster, _ := s.ListAudit(ctx, "", time.Time{}, 10)
	if len(cluster) != 1 || cluster[0].Project != "" {
		t.Fatalf("cluster audit: %+v", cluster)
	}

	// Cleanup: deleting project cascades to keys/quotas/audit.
	if err := s.DeleteAPIKey(ctx, "ck_pg"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupAPIKey(ctx, tok[:]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
