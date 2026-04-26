// Package scheduler runs cron-driven cfunc functions. Jobs are persisted
// as JSON; at runtime they are registered with robfig/cron/v3 and each
// firing performs an HTTP call to the gateway, traversing exactly the
// same spawn-on-demand path as an external request.
//
// The scheduler is intentionally narrow: it does not manage retries,
// jitter, or distributed locking yet. One scheduler instance per
// gateway, single-tenant.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Job is one scheduled invocation.
type Job struct {
	ID       string            `json:"id"`
	Schedule string            `json:"schedule"` // cron expression (5 or 6 fields)
	Function string            `json:"function"` // gateway function name
	Method   string            `json:"method,omitempty"`
	Body     string            `json:"body,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// Validate checks structural correctness; schedule parsing is deferred
// to the caller (uses cron.Parser to surface a precise error).
func (j *Job) Validate() error {
	if j.ID == "" {
		return fmt.Errorf("scheduler: job ID required")
	}
	if j.Schedule == "" {
		return fmt.Errorf("scheduler: schedule required")
	}
	if j.Function == "" {
		return fmt.Errorf("scheduler: function required")
	}
	return nil
}

// Store persists jobs as a single JSON file. Fine for the single-host
// scope of this phase; swap for SQLite when that scope expands.
type Store struct {
	path string
	mu   sync.Mutex
}

// OpenStore opens (creating if missing) a Store at path.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
			return nil, err
		}
	}
	return &Store{path: path}, nil
}

func (s *Store) load() ([]Job, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var jobs []Job
	if err := json.Unmarshal(b, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) save(jobs []Job) error {
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	b, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Add inserts j; rejects duplicate ID.
func (s *Store) Add(j Job) error {
	if err := j.Validate(); err != nil {
		return err
	}
	if _, err := cron.ParseStandard(j.Schedule); err != nil {
		return fmt.Errorf("scheduler: invalid schedule %q: %w", j.Schedule, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return err
	}
	for _, existing := range jobs {
		if existing.ID == j.ID {
			return fmt.Errorf("scheduler: job %q already exists", j.ID)
		}
	}
	jobs = append(jobs, j)
	return s.save(jobs)
}

// Remove deletes a job by ID. Missing IDs are not an error.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return err
	}
	out := jobs[:0]
	for _, j := range jobs {
		if j.ID != id {
			out = append(out, j)
		}
	}
	return s.save(out)
}

// List returns all jobs sorted by ID.
func (s *Store) List() ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Trigger sends the job's HTTP request. Exposed so tests can drive a
// fire without waiting for the cron tick.
type Trigger interface {
	Fire(ctx context.Context, j Job) error
}

// HTTPTrigger calls a gateway over HTTP.
type HTTPTrigger struct {
	BaseURL string // e.g. "http://127.0.0.1:8080"
	Client  *http.Client
}

func (t *HTTPTrigger) Fire(ctx context.Context, j Job) error {
	method := j.Method
	if method == "" {
		method = "POST"
	}
	url := t.BaseURL + "/fn/" + j.Function
	req, err := http.NewRequestWithContext(ctx, method, url, stringReader(j.Body))
	if err != nil {
		return err
	}
	for k, v := range j.Headers {
		req.Header.Set(k, v)
	}
	cl := t.Client
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("scheduler: %s returned %d", j.Function, resp.StatusCode)
	}
	return nil
}

// Scheduler ties a Store to a Trigger via robfig/cron. Calling Start
// loads the store and registers all jobs; subsequent Reload re-syncs.
type Scheduler struct {
	store   *Store
	trigger Trigger
	cron    *cron.Cron
	logger  *slog.Logger

	mu      sync.Mutex
	entries map[string]cron.EntryID // job.ID -> cron entry
}

// New constructs a Scheduler.
func New(store *Store, trigger Trigger, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		store:   store,
		trigger: trigger,
		cron:    cron.New(),
		logger:  logger,
		entries: map[string]cron.EntryID{},
	}
}

// Start loads the persisted job set and begins ticking.
func (s *Scheduler) Start() error {
	if err := s.Reload(); err != nil {
		return err
	}
	s.cron.Start()
	return nil
}

// Stop halts the cron loop and waits for in-flight jobs to finish.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

// Reload syncs the running cron entries with the on-disk Store.
// Removed jobs lose their schedule; added jobs gain one; modified jobs
// are reseated (cron.EntryID changes).
func (s *Scheduler) Reload() error {
	jobs, err := s.store.List()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	desired := map[string]Job{}
	for _, j := range jobs {
		desired[j.ID] = j
	}

	// Remove gone or changed.
	for id, eid := range s.entries {
		if want, ok := desired[id]; !ok || !sameSchedule(want, eid, s.cron) {
			s.cron.Remove(eid)
			delete(s.entries, id)
		}
	}
	// Add new.
	for id, j := range desired {
		if _, ok := s.entries[id]; ok {
			continue
		}
		jobCopy := j
		eid, err := s.cron.AddFunc(j.Schedule, func() { s.run(jobCopy) })
		if err != nil {
			return fmt.Errorf("scheduler: register %s: %w", id, err)
		}
		s.entries[id] = eid
	}
	return nil
}

// FireNow triggers job id immediately, bypassing the cron schedule.
// Useful for tests and CLI "run now".
func (s *Scheduler) FireNow(ctx context.Context, id string) error {
	jobs, err := s.store.List()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.ID == id {
			return s.trigger.Fire(ctx, j)
		}
	}
	return fmt.Errorf("scheduler: job %q not found", id)
}

func (s *Scheduler) run(j Job) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t0 := time.Now()
	err := s.trigger.Fire(ctx, j)
	if err != nil {
		s.logger.Error("cron fire", "job", j.ID, "fn", j.Function, "err", err.Error(),
			"duration_ms", time.Since(t0).Milliseconds())
		return
	}
	s.logger.Info("cron fire", "job", j.ID, "fn", j.Function,
		"duration_ms", time.Since(t0).Milliseconds())
}

// sameSchedule compares the persisted job's schedule to what the cron
// engine currently has. Used to detect edits during Reload.
func sameSchedule(want Job, eid cron.EntryID, c *cron.Cron) bool {
	// robfig/cron doesn't expose the original spec string, so we compare
	// the next fire time of a freshly parsed schedule with the live one.
	parsed, err := cron.ParseStandard(want.Schedule)
	if err != nil {
		return false
	}
	live := c.Entry(eid).Schedule
	if live == nil {
		return false
	}
	now := time.Now()
	return parsed.Next(now).Equal(live.Next(now))
}

func stringReader(s string) *stringReaderImpl {
	return &stringReaderImpl{s: s}
}

type stringReaderImpl struct {
	s string
	i int
}

func (r *stringReaderImpl) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}
