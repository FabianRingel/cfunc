// SPDX-License-Identifier: Apache-2.0

// Package quota implements per-project request-rate limiting backed by
// state.Store. Each gateway maintains in-memory counters; a periodic
// flush pushes the counts into cfunc_quota_usage and refreshes the
// cluster-wide aggregate for enforcement decisions.
//
// Enforcement is approximate by design: under load, multiple gateways
// can each blow past the limit by their own local counter before the
// next sync. The user accepted this trade-off in exchange for a fast
// hot path (no DB hit per request).
package quota

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fabianringel/cfunc/internal/state"
)

// KindRequestsPerMin is the rate-limit kind enforced by the gateway
// middleware. Other kinds (ram_mb, layer_bytes) are tracked but not
// rate-limited at request time.
const KindRequestsPerMin = "requests_per_min"

// Limiter holds in-memory counters per (project, kind) and syncs them
// to a state.Store every Interval. New() returns one ready to Run.
type Limiter struct {
	store    state.Store
	interval time.Duration
	now      func() time.Time

	mu       sync.Mutex
	local    map[string]map[string]*int64 // project → kind → counter (atomic add)
	remote   map[string]map[string]int64  // last-flushed cluster aggregate for current bucket
	limits   map[string]map[string]int64  // last-known configured limit
	lastSync time.Time

	stopCh chan struct{}
	doneCh chan struct{}
}

// Options configure a Limiter. All fields are optional — sensible
// defaults apply.
type Options struct {
	// Interval between flushes. Default: 10s.
	Interval time.Duration
	// Now is overridable for tests. Default: time.Now.
	Now func() time.Time
}

// New constructs a Limiter. Call Run to start the background sync loop;
// the caller is responsible for stopping via Close.
func New(store state.Store, opts Options) *Limiter {
	if opts.Interval == 0 {
		opts.Interval = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Limiter{
		store:    store,
		interval: opts.Interval,
		now:      opts.Now,
		local:    map[string]map[string]*int64{},
		remote:   map[string]map[string]int64{},
		limits:   map[string]map[string]int64{},
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Run starts the background sync loop. Returns immediately.
func (l *Limiter) Run(ctx context.Context) {
	go l.loop(ctx)
}

// Close stops the sync loop and flushes any pending counters.
func (l *Limiter) Close(ctx context.Context) {
	close(l.stopCh)
	<-l.doneCh
	_ = l.flush(ctx)
}

// Allow reports whether one more event of (project, kind) is permitted.
// It increments the local counter when returning true. A zero-valued
// configured limit means unlimited.
//
// The decision uses the most recently synced cluster aggregate plus
// the current gateway's local delta. There is no per-request DB hit.
func (l *Limiter) Allow(project, kind string) bool {
	l.mu.Lock()
	limit := l.limits[project][kind]
	remote := l.remote[project][kind]
	cnt := l.getOrCreateCounter(project, kind)
	l.mu.Unlock()

	if limit <= 0 {
		atomic.AddInt64(cnt, 1)
		return true
	}
	// CAS loop: read current, decide, swap. The "AddInt64-then-revert"
	// shape we used to have produced both spurious denials *and*
	// spurious admits under contention because two goroutines could
	// simultaneously cross the limit and either both revert (false
	// denial) or both observe a post-revert state (false admit).
	// CompareAndSwap gives us a single atomic decision per Allow.
	for {
		cur := atomic.LoadInt64(cnt)
		if remote+cur+1 > limit {
			return false
		}
		if atomic.CompareAndSwapInt64(cnt, cur, cur+1) {
			return true
		}
		// CAS failed: another goroutine raced ahead. Retry with the
		// fresh value; the loop terminates because either we succeed
		// or we overshoot the limit and return false.
	}
}

// Observe is a non-rate-limited counter for kinds that are reported
// after the fact (ram_mb deltas, layer_bytes additions).
func (l *Limiter) Observe(project, kind string, delta int64) {
	l.mu.Lock()
	cnt := l.getOrCreateCounter(project, kind)
	l.mu.Unlock()
	atomic.AddInt64(cnt, delta)
}

func (l *Limiter) getOrCreateCounter(project, kind string) *int64 {
	if _, ok := l.local[project]; !ok {
		l.local[project] = map[string]*int64{}
	}
	if c, ok := l.local[project][kind]; ok {
		return c
	}
	var c int64
	l.local[project][kind] = &c
	return &c
}

func (l *Limiter) loop(ctx context.Context) {
	defer close(l.doneCh)
	t := time.NewTicker(l.interval)
	defer t.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			if err := l.flush(ctx); err != nil {
				// best-effort; the next tick will retry
				_ = err
			}
		}
	}
}

// flush snapshots local counters, pushes them to the store, and
// refreshes the cluster aggregate + configured limits.
func (l *Limiter) flush(ctx context.Context) error {
	bucket := l.now().UTC().Truncate(time.Minute)

	type sample struct {
		project, kind string
		delta         int64
	}
	var samples []sample

	l.mu.Lock()
	for project, byKind := range l.local {
		for kind, cnt := range byKind {
			d := atomic.SwapInt64(cnt, 0)
			if d > 0 {
				samples = append(samples, sample{project, kind, d})
			}
		}
	}
	l.mu.Unlock()

	for _, s := range samples {
		_ = l.store.AddQuotaUsage(ctx, s.project, s.kind, bucket, s.delta)
	}

	// Refresh known limits + aggregates for every project the store
	// knows about. ListProjects is one small query; doing this lets us
	// enforce limits on the very first Allow after creation, before any
	// local counter has rolled over.
	allProjects, err := l.store.ListProjects(ctx)
	if err != nil {
		return err
	}
	projects := make([]string, 0, len(allProjects))
	for _, p := range allProjects {
		projects = append(projects, p.Name)
	}

	for _, p := range projects {
		quotas, err := l.store.ListQuotas(ctx, p)
		if err != nil {
			continue
		}
		l.mu.Lock()
		if _, ok := l.limits[p]; !ok {
			l.limits[p] = map[string]int64{}
		}
		for _, q := range quotas {
			l.limits[p][q.Kind] = q.Value
		}
		l.mu.Unlock()

		for _, q := range quotas {
			used, err := l.store.GetQuotaUsage(ctx, p, q.Kind, bucket)
			if err != nil {
				continue
			}
			l.mu.Lock()
			if _, ok := l.remote[p]; !ok {
				l.remote[p] = map[string]int64{}
			}
			l.remote[p][q.Kind] = used
			l.mu.Unlock()
		}
	}
	l.lastSync = l.now()
	return nil
}

// SetLimit is a test helper that stamps the in-memory limit cache
// without touching the store. Production code calls SetQuota on the
// store and waits for the next flush tick.
func (l *Limiter) SetLimit(project, kind string, value int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.limits[project]; !ok {
		l.limits[project] = map[string]int64{}
	}
	l.limits[project][kind] = value
}
