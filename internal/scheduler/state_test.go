// SPDX-License-Identifier: Apache-2.0

package scheduler_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fabianringel/cfunc/internal/scheduler"
	"github.com/fabianringel/cfunc/internal/state"
)

// stateStubTrigger is a thread-safe Trigger that counts fires per job
// and signals a fire via fireSig (best-effort).
type stateStubTrigger struct {
	count   atomic.Int32
	fireSig chan string
}

func newStateStubTrigger() *stateStubTrigger {
	return &stateStubTrigger{fireSig: make(chan string, 32)}
}

func (s *stateStubTrigger) Fire(_ context.Context, j scheduler.Job) error {
	s.count.Add(1)
	select {
	case s.fireSig <- j.ID:
	default:
	}
	return nil
}

// fakeLeader returns a LeaderElector whose acquisition behaviour is
// fixed at construction time. Lets tests force "I am / am not leader".
type fakeLeader struct{ acquire bool }

func (f fakeLeader) TryAcquireLeadership(_ context.Context, _ string) (func(), error) {
	if !f.acquire {
		return nil, scheduler.ErrLeadershipHeldElsewhere
	}
	return func() {}, nil
}

// TestSchedulerWithStateStore_LoadsExistingJobs proves that a scheduler
// constructed against a populated state.Store sees the existing jobs
// (no scheduler.Store JSON involved).
func TestSchedulerWithStateStore_LoadsExistingJobs(t *testing.T) {
	store := state.NewInMemStore()
	defer store.Close()
	ctx := context.Background()
	if err := store.PutCronJob(ctx, state.CronJob{
		ID: "fast", Schedule: "@every 1s", Function: "f",
	}); err != nil {
		t.Fatal(err)
	}

	tg := newStateStubTrigger()
	sch := scheduler.NewWithState(store, tg, fakeLeader{acquire: true}, nil)
	if err := sch.Start(); err != nil {
		t.Fatal(err)
	}
	defer sch.Stop()

	select {
	case <-tg.fireSig:
		// loaded and fired
	case <-time.After(3 * time.Second):
		t.Fatal("preloaded job did not fire within 3s")
	}
}

// TestSchedulerWithStateStore_PicksUpNewJob proves that a job written
// to the state.Store after the scheduler starts is picked up via
// Watch and fires.
func TestSchedulerWithStateStore_PicksUpNewJob(t *testing.T) {
	store := state.NewInMemStore()
	defer store.Close()
	tg := newStateStubTrigger()
	sch := scheduler.NewWithState(store, tg, fakeLeader{acquire: true}, nil)
	if err := sch.Start(); err != nil {
		t.Fatal(err)
	}
	defer sch.Stop()

	// Add the job *after* the scheduler is up.
	if err := store.PutCronJob(context.Background(), state.CronJob{
		ID: "added-late", Schedule: "@every 1s", Function: "f",
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-tg.fireSig:
	case <-time.After(3 * time.Second):
		t.Fatal("late-added job did not fire within 3s")
	}
}

// TestSchedulerWithStateStore_NonLeaderDoesNotFire proves the central
// cluster property: a scheduler that is not the leader does not fire
// jobs, even though it watches the same store and sees the schedules.
func TestSchedulerWithStateStore_NonLeaderDoesNotFire(t *testing.T) {
	store := state.NewInMemStore()
	defer store.Close()
	_ = store.PutCronJob(context.Background(), state.CronJob{
		ID: "shouldnt-fire", Schedule: "@every 500ms", Function: "f",
	})

	tg := newStateStubTrigger()
	sch := scheduler.NewWithState(store, tg, fakeLeader{acquire: false}, nil)
	if err := sch.Start(); err != nil {
		t.Fatal(err)
	}
	defer sch.Stop()

	// Wait long enough that 2-3 fires would have happened if leader.
	time.Sleep(2 * time.Second)
	if got := tg.count.Load(); got != 0 {
		t.Fatalf("non-leader fired %d times; expected 0", got)
	}
}

// TestSchedulerWithStateStore_DropsRemoved proves that a job deleted
// from the store stops firing on all schedulers within a watch tick.
func TestSchedulerWithStateStore_DropsRemoved(t *testing.T) {
	store := state.NewInMemStore()
	defer store.Close()
	_ = store.PutCronJob(context.Background(), state.CronJob{
		ID: "doomed", Schedule: "@every 500ms", Function: "f",
	})

	tg := newStateStubTrigger()
	sch := scheduler.NewWithState(store, tg, fakeLeader{acquire: true}, nil)
	_ = sch.Start()
	defer sch.Stop()

	// Confirm it fires at least once.
	select {
	case <-tg.fireSig:
	case <-time.After(2 * time.Second):
		t.Fatal("doomed job didn't fire before delete")
	}

	_ = store.DeleteCronJob(context.Background(), "doomed")

	// Drain pending fires, then ensure we don't see new ones.
	time.Sleep(200 * time.Millisecond)
	for {
		select {
		case <-tg.fireSig:
		default:
			goto checkpoint
		}
	}
checkpoint:
	before := tg.count.Load()
	time.Sleep(1500 * time.Millisecond)
	after := tg.count.Load()
	if after > before {
		t.Fatalf("scheduler kept firing deleted job: %d → %d", before, after)
	}
}
