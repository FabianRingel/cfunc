// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestInMemPutGetFunction(t *testing.T) {
	s := NewInMemStore()
	defer s.Close()
	ctx := context.Background()

	def := FunctionDef{Name: "hello", Binary: "/x", MaxConcurrency: 4}
	if err := s.PutFunction(ctx, def); err != nil {
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
		t.Fatal("UpdatedAt not set by Put")
	}
	if got.Project != "default" {
		t.Fatalf("Project default not applied, got %q", got.Project)
	}
}

func TestInMemGetMissing(t *testing.T) {
	s := NewInMemStore()
	defer s.Close()
	_, err := s.GetFunction(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestInMemListFunctionsReturnsAll(t *testing.T) {
	s := NewInMemStore()
	defer s.Close()
	ctx := context.Background()
	for _, n := range []string{"a", "b", "c"} {
		_ = s.PutFunction(ctx, FunctionDef{Name: n, Binary: "/" + n})
	}
	list, err := s.ListFunctions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	for _, d := range list {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	if got := names; len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestInMemDeleteFunction(t *testing.T) {
	s := NewInMemStore()
	defer s.Close()
	ctx := context.Background()
	_ = s.PutFunction(ctx, FunctionDef{Name: "x", Binary: "/x"})
	if err := s.DeleteFunction(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetFunction(ctx, "x")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v after delete", err)
	}
	// Deleting a missing entry is not an error.
	if err := s.DeleteFunction(ctx, "missing"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestInMemPutFunctionIsCopy(t *testing.T) {
	// Mutating the input slice after Put must not corrupt the stored state.
	s := NewInMemStore()
	defer s.Close()
	ctx := context.Background()
	env := []string{"A=1"}
	_ = s.PutFunction(ctx, FunctionDef{Name: "x", Binary: "/x", Env: env})
	env[0] = "MUTATED"

	got, _ := s.GetFunction(ctx, "x")
	if got.Env[0] != "A=1" {
		t.Fatalf("Store leaked input slice: %v", got.Env)
	}
}

func TestInMemWatchReceivesPut(t *testing.T) {
	s := NewInMemStore()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := s.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = s.PutFunction(ctx, FunctionDef{Name: "watched", Binary: "/x"}) }()

	select {
	case ev := <-ch:
		if ev.Kind != EventFunctionPut || ev.Name != "watched" {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event delivered")
	}
}

func TestInMemWatchReceivesDelete(t *testing.T) {
	s := NewInMemStore()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = s.PutFunction(ctx, FunctionDef{Name: "x", Binary: "/x"})
	ch, _ := s.Watch(ctx)

	go func() { _ = s.DeleteFunction(ctx, "x") }()

	select {
	case ev := <-ch:
		if ev.Kind != EventFunctionDelete || ev.Name != "x" {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event delivered")
	}
}

func TestInMemWatchSubscriberCancellation(t *testing.T) {
	s := NewInMemStore()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := s.Watch(ctx)

	cancel()

	// Channel must close.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close on ctx cancel")
	}
}

func TestInMemWatchMultipleSubscribers(t *testing.T) {
	s := NewInMemStore()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const n = 5
	chs := make([]<-chan Event, n)
	for i := 0; i < n; i++ {
		ch, _ := s.Watch(ctx)
		chs[i] = ch
	}

	_ = s.PutFunction(ctx, FunctionDef{Name: "x", Binary: "/x"})

	var wg sync.WaitGroup
	wg.Add(n)
	for _, ch := range chs {
		ch := ch
		go func() {
			defer wg.Done()
			select {
			case ev := <-ch:
				if ev.Name != "x" {
					t.Errorf("subscriber got %+v", ev)
				}
			case <-time.After(2 * time.Second):
				t.Error("subscriber timeout")
			}
		}()
	}
	wg.Wait()
}

func TestInMemCronCRUD(t *testing.T) {
	s := NewInMemStore()
	defer s.Close()
	ctx := context.Background()

	job := CronJob{ID: "daily", Schedule: "0 9 * * *", Function: "report"}
	if err := s.PutCronJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCronJob(ctx, "daily")
	if err != nil {
		t.Fatal(err)
	}
	if got.Schedule != "0 9 * * *" {
		t.Fatalf("got %+v", got)
	}
	list, _ := s.ListCronJobs(ctx)
	if len(list) != 1 {
		t.Fatalf("len=%d", len(list))
	}
	if err := s.DeleteCronJob(ctx, "daily"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCronJob(ctx, "daily"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestInMemSlowSubscriberDoesNotBlock(t *testing.T) {
	// A subscriber that never reads must not stop other subscribers
	// from receiving events.
	s := NewInMemStore()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slow, _ := s.Watch(ctx)   // never read
	fast, _ := s.Watch(ctx)
	_ = slow

	// Push enough events to overflow the slow subscriber's buffer.
	for i := 0; i < 100; i++ {
		_ = s.PutFunction(ctx, FunctionDef{Name: "x", Binary: "/x"})
	}

	// Fast subscriber should still see at least one event without
	// hanging.
	select {
	case <-fast:
	case <-time.After(2 * time.Second):
		t.Fatal("fast subscriber starved by slow peer")
	}
}
