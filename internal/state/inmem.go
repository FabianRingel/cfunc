// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"sync"
	"time"
)

// InMemStore is the single-process implementation of Store. Watch
// subscribers receive every mutation made through this instance; there
// is no cross-process awareness (use PostgresStore for that).
//
// Closed channels signal the unsubscribe path: when ctx is cancelled,
// the watcher goroutine drops the subscriber from its set and closes
// the channel.
type InMemStore struct {
	mu        sync.RWMutex
	functions map[string]FunctionDef
	crons     map[string]CronJob

	// Tenancy state (0.3). Lazy-init via ensureTenancyMaps so existing
	// callers that never touch projects pay nothing.
	projects    map[string]Project
	apiKeys     map[string]APIKey
	quotas      map[string]map[string]Quota
	usage       map[string]map[string]map[time.Time]int64
	audit       []AuditEntry
	nextAuditID int64

	subsMu sync.Mutex
	subs   map[chan Event]struct{}
}

// NewInMemStore returns a Store whose state is purely in-process.
func NewInMemStore() *InMemStore {
	return &InMemStore{
		functions: map[string]FunctionDef{},
		crons:     map[string]CronJob{},
		subs:      map[chan Event]struct{}{},
	}
}

func (s *InMemStore) GetFunction(_ context.Context, name string) (FunctionDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.functions[name]
	if !ok {
		return FunctionDef{}, ErrNotFound
	}
	return cloneFunc(d), nil
}

func (s *InMemStore) ListFunctions(_ context.Context) ([]FunctionDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]FunctionDef, 0, len(s.functions))
	for _, d := range s.functions {
		out = append(out, cloneFunc(d))
	}
	return out, nil
}

func (s *InMemStore) PutFunction(_ context.Context, def FunctionDef) error {
	def.UpdatedAt = time.Now().UTC()
	if def.Project == "" {
		def.Project = "default"
	}
	s.mu.Lock()
	s.functions[def.Name] = cloneFunc(def)
	s.mu.Unlock()
	s.broadcast(Event{Kind: EventFunctionPut, Name: def.Name})
	return nil
}

func (s *InMemStore) DeleteFunction(_ context.Context, name string) error {
	s.mu.Lock()
	delete(s.functions, name)
	s.mu.Unlock()
	s.broadcast(Event{Kind: EventFunctionDelete, Name: name})
	return nil
}

func (s *InMemStore) GetCronJob(_ context.Context, id string) (CronJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.crons[id]
	if !ok {
		return CronJob{}, ErrNotFound
	}
	return cloneCron(j), nil
}

func (s *InMemStore) ListCronJobs(_ context.Context) ([]CronJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CronJob, 0, len(s.crons))
	for _, j := range s.crons {
		out = append(out, cloneCron(j))
	}
	return out, nil
}

func (s *InMemStore) PutCronJob(_ context.Context, j CronJob) error {
	j.UpdatedAt = time.Now().UTC()
	if j.Project == "" {
		j.Project = "default"
	}
	s.mu.Lock()
	s.crons[j.ID] = cloneCron(j)
	s.mu.Unlock()
	s.broadcast(Event{Kind: EventCronPut, Name: j.ID})
	return nil
}

func (s *InMemStore) DeleteCronJob(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.crons, id)
	s.mu.Unlock()
	s.broadcast(Event{Kind: EventCronDelete, Name: id})
	return nil
}

// Watch returns a fresh channel; the goroutine cleaning it up exits
// when ctx is cancelled.
func (s *InMemStore) Watch(ctx context.Context) (<-chan Event, error) {
	ch := make(chan Event, 16)
	s.subsMu.Lock()
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()

	go func() {
		<-ctx.Done()
		s.subsMu.Lock()
		delete(s.subs, ch)
		s.subsMu.Unlock()
		close(ch)
	}()
	return ch, nil
}

// Close drops all subscribers. Pending sends are silently discarded —
// this is a hard shutdown, not a graceful drain.
func (s *InMemStore) Close() error {
	s.subsMu.Lock()
	for ch := range s.subs {
		// Drain and close. Goroutines waiting on ctx.Done will also
		// exit independently; this just ensures no goroutine sits on a
		// send to a never-read channel.
		select {
		case <-ch:
		default:
		}
	}
	s.subs = map[chan Event]struct{}{}
	s.subsMu.Unlock()
	return nil
}

// broadcast sends ev to every current subscriber. Slow subscribers
// drop the event rather than block the writer.
func (s *InMemStore) broadcast(ev Event) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
			// subscriber not draining; drop
		}
	}
}

func cloneFunc(d FunctionDef) FunctionDef {
	out := d
	if d.Env != nil {
		out.Env = append([]string(nil), d.Env...)
	}
	if d.Layers != nil {
		out.Layers = append([]LayerMount(nil), d.Layers...)
	}
	return out
}

func cloneCron(j CronJob) CronJob {
	out := j
	if j.Headers != nil {
		out.Headers = make(map[string]string, len(j.Headers))
		for k, v := range j.Headers {
			out.Headers[k] = v
		}
	}
	return out
}
