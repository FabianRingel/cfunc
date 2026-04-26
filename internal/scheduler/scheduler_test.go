// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStoreCRUD(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "cron.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Job{ID: "j1", Schedule: "*/5 * * * *", Function: "f"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Job{ID: "j2", Schedule: "0 * * * *", Function: "g"}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 2 {
		t.Fatalf("got %d", len(list))
	}
	if list[0].ID != "j1" || list[1].ID != "j2" {
		t.Fatalf("not sorted: %+v", list)
	}

	// Duplicate ID rejected.
	if err := s.Add(Job{ID: "j1", Schedule: "* * * * *", Function: "x"}); err == nil {
		t.Fatal("expected duplicate error")
	}

	// Remove.
	if err := s.Remove("j1"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List()
	if len(list) != 1 || list[0].ID != "j2" {
		t.Fatalf("got %+v", list)
	}
}

func TestStoreRejectsInvalidSchedule(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "c.json"))
	if err := s.Add(Job{ID: "x", Schedule: "not a cron", Function: "f"}); err == nil {
		t.Fatal("expected schedule error")
	}
}

func TestStoreValidation(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "c.json"))
	cases := []Job{
		{Schedule: "* * * * *", Function: "f"},   // no ID
		{ID: "x", Function: "f"},                 // no schedule
		{ID: "x", Schedule: "* * * * *"},         // no function
	}
	for i, j := range cases {
		if err := s.Add(j); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

// stubTrigger records every Fire and signals via fired().
type stubTrigger struct {
	mu       sync.Mutex
	jobs     []Job
	fail     bool
	fireSig  chan Job
	firedCnt atomic.Int32
}

func newStubTrigger() *stubTrigger {
	return &stubTrigger{fireSig: make(chan Job, 16)}
}

func (s *stubTrigger) Fire(ctx context.Context, j Job) error {
	s.mu.Lock()
	s.jobs = append(s.jobs, j)
	s.mu.Unlock()
	s.firedCnt.Add(1)
	select {
	case s.fireSig <- j:
	default:
	}
	if s.fail {
		return context.Canceled
	}
	return nil
}

func TestSchedulerFireNow(t *testing.T) {
	store, _ := OpenStore(filepath.Join(t.TempDir(), "c.json"))
	store.Add(Job{ID: "j1", Schedule: "0 0 1 1 *", Function: "fn"}) // never fires soon
	tg := newStubTrigger()
	sch := New(store, tg, nil)
	if err := sch.Start(); err != nil {
		t.Fatal(err)
	}
	defer sch.Stop()

	if err := sch.FireNow(context.Background(), "j1"); err != nil {
		t.Fatal(err)
	}
	if tg.firedCnt.Load() != 1 {
		t.Fatalf("expected 1 fire, got %d", tg.firedCnt.Load())
	}
	if err := sch.FireNow(context.Background(), "missing"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestSchedulerReloadSyncsAdditions(t *testing.T) {
	store, _ := OpenStore(filepath.Join(t.TempDir(), "c.json"))
	tg := newStubTrigger()
	sch := New(store, tg, nil)
	if err := sch.Start(); err != nil {
		t.Fatal(err)
	}
	defer sch.Stop()

	if got := len(sch.entries); got != 0 {
		t.Fatalf("entries=%d", got)
	}
	store.Add(Job{ID: "new", Schedule: "* * * * *", Function: "f"})
	if err := sch.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := len(sch.entries); got != 1 {
		t.Fatalf("entries=%d", got)
	}
}

func TestSchedulerReloadDropsRemoved(t *testing.T) {
	store, _ := OpenStore(filepath.Join(t.TempDir(), "c.json"))
	store.Add(Job{ID: "g", Schedule: "* * * * *", Function: "f"})
	sch := New(store, newStubTrigger(), nil)
	sch.Start()
	defer sch.Stop()

	store.Remove("g")
	sch.Reload()
	if got := len(sch.entries); got != 0 {
		t.Fatalf("entries=%d", got)
	}
}

func TestHTTPTriggerCallsGateway(t *testing.T) {
	var seenMethod, seenPath, seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		seenPath = r.URL.Path
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		seenBody = string(buf[:n])
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tg := &HTTPTrigger{BaseURL: srv.URL}
	if err := tg.Fire(context.Background(), Job{
		ID: "j", Schedule: "* * * * *", Function: "myfn",
		Method: "POST", Body: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	if seenMethod != "POST" || seenPath != "/fn/myfn" || seenBody != "hello" {
		t.Fatalf("got method=%s path=%s body=%q", seenMethod, seenPath, seenBody)
	}
}

func TestHTTPTriggerReportsServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	tg := &HTTPTrigger{BaseURL: srv.URL}
	err := tg.Fire(context.Background(), Job{ID: "j", Schedule: "* * * * *", Function: "fn"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestEverySecondScheduleFires uses a 1-second cron that should fire
// within ~2s. Bounded to avoid flakiness on slow CI.
func TestEverySecondScheduleFires(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	store, _ := OpenStore(filepath.Join(t.TempDir(), "c.json"))
	// 6-field cron with seconds: every second
	store.Add(Job{ID: "fast", Schedule: "@every 1s", Function: "f"})
	tg := newStubTrigger()
	sch := New(store, tg, nil)
	sch.Start()
	defer sch.Stop()

	select {
	case <-tg.fireSig:
		// good
	case <-time.After(3 * time.Second):
		t.Fatal("cron did not fire within 3s")
	}
}
