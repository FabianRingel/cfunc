// SPDX-License-Identifier: Apache-2.0

package quota

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fabianringel/cfunc/internal/state"
)

func atomicAdd(p *int64, n int64) { atomic.AddInt64(p, n) }

func TestAllowUnlimitedWhenNoQuota(t *testing.T) {
	s := state.NewInMemStore()
	l := New(s, Options{})
	for i := 0; i < 1000; i++ {
		if !l.Allow("acme", KindRequestsPerMin) {
			t.Fatalf("denied at i=%d without configured limit", i)
		}
	}
}

func TestAllowEnforcesLimitAfterFlush(t *testing.T) {
	s := state.NewInMemStore()
	ctx := context.Background()
	_ = s.CreateProject(ctx, state.Project{Name: "acme"})
	_ = s.SetQuota(ctx, "acme", KindRequestsPerMin, 5)

	l := New(s, Options{})
	// Initial flush so limits map is populated.
	if err := l.flush(ctx); err != nil {
		t.Fatal(err)
	}

	allowed := 0
	for i := 0; i < 20; i++ {
		if l.Allow("acme", KindRequestsPerMin) {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("got %d allowed, want 5", allowed)
	}
}

func TestFlushSyncsToStore(t *testing.T) {
	s := state.NewInMemStore()
	ctx := context.Background()
	_ = s.CreateProject(ctx, state.Project{Name: "acme"})

	now := time.Now().UTC().Truncate(time.Minute)
	l := New(s, Options{Now: func() time.Time { return now }})

	for i := 0; i < 7; i++ {
		l.Allow("acme", KindRequestsPerMin)
	}
	if err := l.flush(ctx); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetQuotaUsage(ctx, "acme", KindRequestsPerMin, now)
	if got != 7 {
		t.Fatalf("usage in store: got %d, want 7", got)
	}
}

func TestAllowConcurrentRespectsExactLimit(t *testing.T) {
	// 100 goroutines each call Allow once against a per-bucket limit of 50.
	// Exactly 50 must succeed. The pre-CAS implementation produced both
	// false denials and false admits under contention.
	s := state.NewInMemStore()
	ctx := context.Background()
	_ = s.CreateProject(ctx, state.Project{Name: "acme"})
	_ = s.SetQuota(ctx, "acme", KindRequestsPerMin, 50)

	l := New(s, Options{})
	if err := l.flush(ctx); err != nil {
		t.Fatal(err)
	}

	const n = 100
	var allowed int64
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			if l.Allow("acme", KindRequestsPerMin) {
				atomicAdd(&allowed, 1)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	if allowed != 50 {
		t.Fatalf("got %d allowed, want exactly 50", allowed)
	}
}

func TestRemoteAggregateCarriesOver(t *testing.T) {
	// Two limiters sharing one store: each consumes 3, then one of
	// them flushes. After flush, the limiter that did NOT consume yet
	// must observe the cluster aggregate so its decisions reflect peer
	// usage.
	s := state.NewInMemStore()
	ctx := context.Background()
	_ = s.CreateProject(ctx, state.Project{Name: "acme"})
	_ = s.SetQuota(ctx, "acme", KindRequestsPerMin, 5)

	now := time.Now().UTC().Truncate(time.Minute)
	a := New(s, Options{Now: func() time.Time { return now }})
	b := New(s, Options{Now: func() time.Time { return now }})

	// Populate limits on b first so b knows the cap.
	if err := b.flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.flush(ctx); err != nil {
		t.Fatal(err)
	}

	// a consumes 4 and flushes — store now reads 4.
	for i := 0; i < 4; i++ {
		a.Allow("acme", KindRequestsPerMin)
	}
	if err := a.flush(ctx); err != nil {
		t.Fatal(err)
	}

	// b refreshes its remote-aggregate via flush.
	if err := b.flush(ctx); err != nil {
		t.Fatal(err)
	}

	// b should now allow 1 more (5 cap - 4 already used by a) and deny
	// the second.
	if !b.Allow("acme", KindRequestsPerMin) {
		t.Fatal("b should have allowed the 5th request cluster-wide")
	}
	if b.Allow("acme", KindRequestsPerMin) {
		t.Fatal("b should have denied the 6th cluster-wide request")
	}
}
