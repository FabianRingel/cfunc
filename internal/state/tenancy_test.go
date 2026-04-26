// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"testing"
	"time"
)

// All tenancy tests run against InMemStore. The Postgres backend is
// covered by smoke tests in postgres_test.go that exercise the same
// surface — keeping the unit tests in-memory keeps them fast.

func TestProjectCRUD(t *testing.T) {
	s := NewInMemStore()
	ctx := context.Background()

	if err := s.CreateProject(ctx, Project{Name: "acme", Description: "test"}); err != nil {
		t.Fatal(err)
	}
	// Duplicate is an error.
	if err := s.CreateProject(ctx, Project{Name: "acme"}); err == nil {
		t.Fatal("expected duplicate project error")
	}

	p, err := s.GetProject(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "acme" || p.Description != "test" {
		t.Fatalf("got %+v", p)
	}
	if p.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not stamped")
	}

	if _, err := s.GetProject(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}

	ps, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Name != "acme" {
		t.Fatalf("got %+v", ps)
	}

	if err := s.DeleteProject(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProject(ctx, "acme"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestAPIKeyCRUD(t *testing.T) {
	s := NewInMemStore()
	ctx := context.Background()
	mustCreateProject(t, s, "acme")

	tok := sha256.Sum256([]byte("plaintext-token-123"))
	key := APIKey{
		ID:          "ck_one",
		Project:     "acme",
		Description: "ci",
		TokenSHA256: tok[:],
		Scopes:      []string{"deploy", "invoke"},
	}
	if err := s.CreateAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	// Duplicate id rejected.
	if err := s.CreateAPIKey(ctx, key); err == nil {
		t.Fatal("expected duplicate key id error")
	}

	got, err := s.LookupAPIKey(ctx, tok[:])
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "ck_one" || got.Project != "acme" {
		t.Fatalf("lookup got %+v", got)
	}
	if got.LastUsedAt == nil || got.LastUsedAt.IsZero() {
		t.Fatal("LookupAPIKey should bump LastUsedAt")
	}
	if !hasScope(got.Scopes, "deploy") || hasScope(got.Scopes, "admin") {
		t.Fatalf("scopes wrong: %v", got.Scopes)
	}

	other := sha256.Sum256([]byte("nope"))
	if _, err := s.LookupAPIKey(ctx, other[:]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup unknown: %v", err)
	}

	keys, err := s.ListAPIKeys(ctx, "acme")
	if err != nil || len(keys) != 1 {
		t.Fatalf("list got %v %v", keys, err)
	}
	// TokenSHA256 must round-trip; required for migration / display.
	if string(keys[0].TokenSHA256) != string(tok[:]) {
		t.Fatal("token hash not preserved")
	}

	if err := s.DeleteAPIKey(ctx, "ck_one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupAPIKey(ctx, tok[:]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestQuotaSetGetList(t *testing.T) {
	s := NewInMemStore()
	ctx := context.Background()
	mustCreateProject(t, s, "acme")

	if err := s.SetQuota(ctx, "acme", "requests_per_min", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuota(ctx, "acme", "ram_mb", 2048); err != nil {
		t.Fatal(err)
	}

	v, err := s.GetQuota(ctx, "acme", "requests_per_min")
	if err != nil || v != 1000 {
		t.Fatalf("got %d %v", v, err)
	}

	// Update overwrites.
	if err := s.SetQuota(ctx, "acme", "requests_per_min", 2000); err != nil {
		t.Fatal(err)
	}
	v, _ = s.GetQuota(ctx, "acme", "requests_per_min")
	if v != 2000 {
		t.Fatalf("update did not persist: %d", v)
	}

	// Unknown kind returns 0 (unlimited) without error.
	v, err = s.GetQuota(ctx, "acme", "unknown")
	if err != nil || v != 0 {
		t.Fatalf("unknown got %d %v", v, err)
	}

	qs, _ := s.ListQuotas(ctx, "acme")
	if len(qs) != 2 {
		t.Fatalf("list got %v", qs)
	}
}

func TestQuotaUsageRollup(t *testing.T) {
	s := NewInMemStore()
	ctx := context.Background()
	mustCreateProject(t, s, "acme")

	now := time.Now().UTC().Truncate(time.Minute)
	if err := s.AddQuotaUsage(ctx, "acme", "requests_per_min", now, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.AddQuotaUsage(ctx, "acme", "requests_per_min", now, 7); err != nil {
		t.Fatal(err)
	}
	// Older bucket — must not contribute when querying the current minute.
	if err := s.AddQuotaUsage(ctx, "acme", "requests_per_min",
		now.Add(-2*time.Minute), 99); err != nil {
		t.Fatal(err)
	}

	v, err := s.GetQuotaUsage(ctx, "acme", "requests_per_min", now)
	if err != nil {
		t.Fatal(err)
	}
	if v != 12 {
		t.Fatalf("got %d, want 12 (5+7)", v)
	}

	// Querying a wider window pulls the older bucket too.
	v, _ = s.GetQuotaUsage(ctx, "acme", "requests_per_min", now.Add(-5*time.Minute))
	if v != 12+99 {
		t.Fatalf("wider window got %d, want %d", v, 12+99)
	}
}

func TestAuditAppendList(t *testing.T) {
	s := NewInMemStore()
	ctx := context.Background()
	mustCreateProject(t, s, "acme")

	for i, action := range []string{"function.put", "key.create", "quota.set"} {
		err := s.AppendAudit(ctx, AuditEntry{
			Project: "acme",
			Actor:   "ck_one",
			Action:  action,
			Target:  "t" + string(rune('0'+i)),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := s.ListAudit(ctx, "acme", time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries", len(entries))
	}
	// Newest first.
	for i := 1; i < len(entries); i++ {
		if entries[i-1].TS.Before(entries[i].TS) {
			t.Fatal("entries not sorted newest-first")
		}
	}
	if entries[0].Action != "quota.set" {
		t.Fatalf("newest entry wrong: %+v", entries[0])
	}

	// Limit truncates.
	limited, _ := s.ListAudit(ctx, "acme", time.Time{}, 2)
	if len(limited) != 2 {
		t.Fatalf("limit not respected: %d", len(limited))
	}

	// Project filter: empty project means cluster-level entries only.
	if err := s.AppendAudit(ctx, AuditEntry{
		Actor:  "admin",
		Action: "project.create",
		Target: "beta",
	}); err != nil {
		t.Fatal(err)
	}
	cluster, _ := s.ListAudit(ctx, "", time.Time{}, 100)
	if len(cluster) != 1 || cluster[0].Action != "project.create" {
		t.Fatalf("cluster filter got %+v", cluster)
	}
}

// helpers --------------------------------------------------------------

func mustCreateProject(t *testing.T, s Store, name string) {
	t.Helper()
	if err := s.CreateProject(context.Background(), Project{Name: name}); err != nil {
		t.Fatal(err)
	}
}

func hasScope(scopes []string, want string) bool {
	sort.Strings(scopes)
	i := sort.SearchStrings(scopes, want)
	return i < len(scopes) && scopes[i] == want
}
