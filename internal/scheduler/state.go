// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/fabianringel/cfunc/internal/state"
)

// LeaderElector decides whether this scheduler instance is allowed to
// fire jobs. PostgresStore implements this via pg_try_advisory_lock;
// single-node deployments can supply a NopLeader that always accepts.
type LeaderElector interface {
	TryAcquireLeadership(ctx context.Context, key string) (release func(), err error)
}

// ErrLeadershipHeldElsewhere is returned by LeaderElector
// implementations when a peer holds the lock.
var ErrLeadershipHeldElsewhere = errors.New("scheduler: leadership held elsewhere")

// NopLeader always becomes the leader. Use this when running a single
// scheduler instance (no need for pg_try_advisory_lock overhead).
type NopLeader struct{}

func (NopLeader) TryAcquireLeadership(_ context.Context, _ string) (func(), error) {
	return func() {}, nil
}

// LeadershipKey is the advisory-lock identifier scheduler uses across
// the cluster. Exported so operators with multiple unrelated cron
// daemons against the same DB don't collide.
const LeadershipKey = "cfunc/scheduler/leader"

// leaderRetryInterval controls how often a non-leader retries to take
// leadership when the current leader goes down.
const leaderRetryInterval = 5 * time.Second

// StateScheduler is the cluster-aware scheduler. It uses a state.Store
// for cron-job persistence and a LeaderElector to gate firing.
//
// The instance maintains a local cron.Cron that holds entries for all
// known jobs but only the leader's run() function actually invokes the
// trigger. Non-leaders Watch the store too, so they're ready to take
// over instantly when leadership flips.
type StateScheduler struct {
	store    state.Store
	trigger  Trigger
	elector  LeaderElector
	logger   *slog.Logger
	cron     *cron.Cron

	mu       sync.Mutex
	entries  map[string]cron.EntryID
	specs    map[string]string // last-seen schedule per id, for diff
	isLeader bool
	release  func()

	watchCtx    context.Context
	watchCancel context.CancelFunc
	watchCh     <-chan state.Event

	stopCh chan struct{}
	doneCh chan struct{}
}

// NewWithState builds a state-backed scheduler. logger may be nil
// (defaults to slog.Default).
func NewWithState(store state.Store, trigger Trigger, elector LeaderElector, logger *slog.Logger) *StateScheduler {
	if logger == nil {
		logger = slog.Default()
	}
	if elector == nil {
		elector = NopLeader{}
	}
	return &StateScheduler{
		store:   store,
		trigger: trigger,
		elector: elector,
		logger:  logger,
		cron:    cron.New(),
		entries: map[string]cron.EntryID{},
		specs:   map[string]string{},
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

// Start runs initial reconciliation, attempts leadership, subscribes
// to store events, and begins watching. Subscription happens *before*
// Start returns so callers that immediately Put a job don't race with
// the goroutine launch.
func (s *StateScheduler) Start() error {
	s.watchCtx, s.watchCancel = context.WithCancel(context.Background())
	ch, err := s.store.Watch(s.watchCtx)
	if err != nil {
		return fmt.Errorf("scheduler: watch: %w", err)
	}
	s.watchCh = ch

	if err := s.reconcile(context.Background()); err != nil {
		return err
	}
	s.tryLeader(context.Background())
	s.cron.Start()
	go s.run()
	return nil
}

// Stop halts watching and cron, releases leadership.
func (s *StateScheduler) Stop() {
	close(s.stopCh)
	<-s.doneCh
	s.cron.Stop()
	if s.watchCancel != nil {
		s.watchCancel()
	}
	s.mu.Lock()
	if s.release != nil {
		s.release()
		s.release = nil
		s.isLeader = false
	}
	s.mu.Unlock()
}

// run is the long-lived loop: it consumes the pre-established Watch
// channel and periodically retries leadership.
func (s *StateScheduler) run() {
	defer close(s.doneCh)
	leaderTick := time.NewTicker(leaderRetryInterval)
	defer leaderTick.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case ev, ok := <-s.watchCh:
			if !ok {
				return
			}
			switch ev.Kind {
			case state.EventCronPut, state.EventCronDelete:
				if err := s.reconcile(s.watchCtx); err != nil {
					s.logger.Warn("scheduler: reconcile after watch", "err", err)
				}
			}
		case <-leaderTick.C:
			s.mu.Lock()
			isLeader := s.isLeader
			s.mu.Unlock()
			if !isLeader {
				s.tryLeader(s.watchCtx)
			}
		}
	}
}

// tryLeader attempts to claim leadership and records the result.
func (s *StateScheduler) tryLeader(ctx context.Context) {
	release, err := s.elector.TryAcquireLeadership(ctx, LeadershipKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if s.isLeader {
			s.logger.Info("scheduler: lost leadership")
		}
		s.isLeader = false
		return
	}
	if !s.isLeader {
		s.logger.Info("scheduler: acquired leadership")
	}
	s.isLeader = true
	s.release = release
}

// reconcile syncs the cron engine with what's currently in the store:
// add new jobs, drop deleted ones, reseat schedule changes.
func (s *StateScheduler) reconcile(ctx context.Context) error {
	jobs, err := s.store.ListCronJobs(ctx)
	if err != nil {
		return err
	}
	desired := map[string]state.CronJob{}
	for _, j := range jobs {
		desired[j.ID] = j
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove jobs that disappeared or whose schedule changed.
	for id, eid := range s.entries {
		want, present := desired[id]
		if !present || want.Schedule != s.specs[id] {
			s.cron.Remove(eid)
			delete(s.entries, id)
			delete(s.specs, id)
		}
	}
	// Add or re-add jobs.
	for id, j := range desired {
		if _, ok := s.entries[id]; ok {
			continue
		}
		jobCopy := j
		eid, err := s.cron.AddFunc(j.Schedule, func() { s.fire(jobCopy) })
		if err != nil {
			return fmt.Errorf("scheduler: register %s: %w", id, err)
		}
		s.entries[id] = eid
		s.specs[id] = j.Schedule
	}
	return nil
}

// fire is the cron-tick entrypoint. Skips firing on non-leaders.
func (s *StateScheduler) fire(j state.CronJob) {
	s.mu.Lock()
	leader := s.isLeader
	s.mu.Unlock()
	if !leader {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t0 := time.Now()
	err := s.trigger.Fire(ctx, jobFromState(j))
	if err != nil {
		s.logger.Error("scheduler: fire failed",
			"id", j.ID, "fn", j.Function, "err", err.Error(),
			"duration_ms", time.Since(t0).Milliseconds())
		return
	}
	s.logger.Info("scheduler: fire",
		"id", j.ID, "fn", j.Function,
		"duration_ms", time.Since(t0).Milliseconds())
}

func jobFromState(j state.CronJob) Job {
	return Job{
		ID:       j.ID,
		Schedule: j.Schedule,
		Function: j.Function,
		Method:   j.Method,
		Body:     j.Body,
		Headers:  j.Headers,
	}
}
