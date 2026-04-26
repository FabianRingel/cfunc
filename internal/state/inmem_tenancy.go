// SPDX-License-Identifier: Apache-2.0

package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// In-memory tenancy state. Lives on InMemStore as separate maps; the
// existing top-level mutex (s.mu) guards them too. Watch events are
// not emitted for tenancy mutations — there's no hot path that needs
// per-replica invalidation, and admin actions are infrequent enough
// that a periodic refresh on the gateway side is cheap.

func (s *InMemStore) ensureTenancyMaps() {
	if s.projects == nil {
		s.projects = map[string]Project{}
		s.apiKeys = map[string]APIKey{}
		s.quotas = map[string]map[string]Quota{}      // project → kind → quota
		s.usage = map[string]map[string]map[time.Time]int64{} // project → kind → bucket → value
		s.audit = []AuditEntry{}
		s.nextAuditID = 1
	}
}

func (s *InMemStore) CreateProject(_ context.Context, p Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureTenancyMaps()
	if _, ok := s.projects[p.Name]; ok {
		return fmt.Errorf("state: project %q already exists", p.Name)
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	s.projects[p.Name] = p
	return nil
}

func (s *InMemStore) GetProject(_ context.Context, name string) (Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[name]
	if !ok {
		return Project{}, ErrNotFound
	}
	return p, nil
}

func (s *InMemStore) ListProjects(_ context.Context) ([]Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Project, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *InMemStore) DeleteProject(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.projects, name)
	return nil
}

func (s *InMemStore) CreateAPIKey(_ context.Context, k APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureTenancyMaps()
	if _, ok := s.apiKeys[k.ID]; ok {
		return fmt.Errorf("state: api key %q already exists", k.ID)
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now().UTC()
	}
	s.apiKeys[k.ID] = k
	return nil
}

func (s *InMemStore) LookupAPIKey(_ context.Context, tokenSHA256 []byte) (APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, k := range s.apiKeys {
		if bytes.Equal(k.TokenSHA256, tokenSHA256) {
			now := time.Now().UTC()
			k.LastUsedAt = &now
			s.apiKeys[id] = k
			return k, nil
		}
	}
	return APIKey{}, ErrNotFound
}

func (s *InMemStore) ListAPIKeys(_ context.Context, project string) ([]APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []APIKey{}
	for _, k := range s.apiKeys {
		if k.Project == project {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *InMemStore) DeleteAPIKey(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.apiKeys, id)
	return nil
}

func (s *InMemStore) SetQuota(_ context.Context, project, kind string, value int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureTenancyMaps()
	if _, ok := s.quotas[project]; !ok {
		s.quotas[project] = map[string]Quota{}
	}
	s.quotas[project][kind] = Quota{
		Project: project, Kind: kind, Value: value, UpdatedAt: time.Now().UTC(),
	}
	return nil
}

func (s *InMemStore) GetQuota(_ context.Context, project, kind string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if byKind, ok := s.quotas[project]; ok {
		if q, ok := byKind[kind]; ok {
			return q.Value, nil
		}
	}
	return 0, nil
}

func (s *InMemStore) ListQuotas(_ context.Context, project string) ([]Quota, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byKind := s.quotas[project]
	out := make([]Quota, 0, len(byKind))
	for _, q := range byKind {
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

func (s *InMemStore) AddQuotaUsage(_ context.Context, project, kind string, bucket time.Time, delta int64) error {
	if delta < 0 {
		return errors.New("state: AddQuotaUsage delta must be non-negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureTenancyMaps()
	bucket = bucket.UTC().Truncate(time.Minute)
	if _, ok := s.usage[project]; !ok {
		s.usage[project] = map[string]map[time.Time]int64{}
	}
	if _, ok := s.usage[project][kind]; !ok {
		s.usage[project][kind] = map[time.Time]int64{}
	}
	s.usage[project][kind][bucket] += delta
	return nil
}

func (s *InMemStore) GetQuotaUsage(_ context.Context, project, kind string, since time.Time) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	since = since.UTC().Truncate(time.Minute)
	var total int64
	if byKind, ok := s.usage[project]; ok {
		if buckets, ok := byKind[kind]; ok {
			for ts, v := range buckets {
				if !ts.Before(since) {
					total += v
				}
			}
		}
	}
	return total, nil
}

func (s *InMemStore) AppendAudit(_ context.Context, e AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureTenancyMaps()
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	e.ID = s.nextAuditID
	s.nextAuditID++
	s.audit = append(s.audit, e)
	return nil
}

func (s *InMemStore) ListAudit(_ context.Context, project string, since time.Time, limit int) ([]AuditEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []AuditEntry{}
	for _, e := range s.audit {
		if e.Project != project {
			continue
		}
		if !since.IsZero() && e.TS.Before(since) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS.After(out[j].TS) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
