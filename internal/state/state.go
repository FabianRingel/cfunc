// SPDX-License-Identifier: Apache-2.0

// Package state defines the shared-state abstraction the cfunc gateway
// and scheduler use. Two implementations exist:
//
//   - InMemStore: process-local map. Default for single-node mode;
//     identical observable behavior to the pre-cluster code path.
//   - PostgresStore (forthcoming, A2): Postgres-backed, multi-replica
//     coordinated, uses LISTEN/NOTIFY for cross-node invalidation and
//     pg_try_advisory_lock for cron leader-election.
//
// Both implementations satisfy the same Store interface. A gateway
// holds a *single* Store reference; whether functions are stored locally
// or in Postgres is invisible to ServeHTTP.
//
// Cache strategy: the gateway keeps a read-only local map filled from
// ListFunctions() at startup and refreshed via Watch(). All hot-path
// reads (ServeHTTP, Stats) go through the cache, never the Store
// directly — even with a Postgres backend, no DB hit per request.
package state

import (
	"context"
	"errors"
	"time"
)

// FunctionDef is the persistent shape of a registered function.
//
// This duplicates gateway.FunctionDef intentionally: the state package
// must not depend on gateway, and gateway converts between the two at
// its boundary. The fields are otherwise the same plus a few that only
// make sense at the persistence layer (UpdatedAt, Project).
type FunctionDef struct {
	Name           string       `json:"name"`
	Binary         string       `json:"binary"`
	Env            []string     `json:"env,omitempty"`
	Layers         []LayerMount `json:"layers,omitempty"`
	MaxConcurrency int          `json:"max_concurrency,omitempty"`

	// Project is the multi-tenancy key. Phase 0.2 uses "default" for
	// every function; 0.3 wires real projects.
	Project string `json:"project,omitempty"`

	// UpdatedAt is set by the Store on Put; callers don't need to fill it.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// LayerMount mirrors gateway.LayerMount; same field names, same meaning.
//
// Digest is the content-addressed identifier (sha256:…) used for
// layer-distribution: the gateway pulls from layerstore by Digest if
// HostPath isn't populated locally. Empty Digest means "host-local
// only" — the legacy 0.1/0.2 behaviour.
type LayerMount struct {
	Name      string `json:"name"`
	HostPath  string `json:"host_path"`
	MountPath string `json:"mount_path"`
	Digest    string `json:"digest,omitempty"`
}

// CronJob is the persistent shape of a scheduled invocation.
type CronJob struct {
	ID        string            `json:"id"`
	Schedule  string            `json:"schedule"`
	Function  string            `json:"function"`
	Method    string            `json:"method,omitempty"`
	Body      string            `json:"body,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Project   string            `json:"project,omitempty"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
}

// EventKind enumerates Watch deliveries.
type EventKind string

const (
	EventFunctionPut    EventKind = "function.put"
	EventFunctionDelete EventKind = "function.delete"
	EventCronPut        EventKind = "cron.put"
	EventCronDelete     EventKind = "cron.delete"
)

// Event is a single Watch notification. Name identifies the affected
// resource (function name or cron ID).
type Event struct {
	Kind EventKind
	Name string
}

// ErrNotFound is returned by Get* when no matching entry exists.
var ErrNotFound = errors.New("state: not found")

// Store is the shared-state surface used by the gateway and scheduler.
// All methods are safe for concurrent use.
type Store interface {
	// GetFunction returns the named function or ErrNotFound.
	GetFunction(ctx context.Context, name string) (FunctionDef, error)
	// ListFunctions returns all functions. The order is unspecified;
	// callers that need deterministic order must sort.
	ListFunctions(ctx context.Context) ([]FunctionDef, error)
	// PutFunction inserts or replaces a function. UpdatedAt is set by
	// the Store. Emits EventFunctionPut on success.
	PutFunction(ctx context.Context, def FunctionDef) error
	// DeleteFunction removes a function. Missing names are not an error.
	// Emits EventFunctionDelete on success (also for missing names).
	DeleteFunction(ctx context.Context, name string) error

	// GetCronJob, ListCronJobs, PutCronJob, DeleteCronJob mirror the
	// function methods.
	GetCronJob(ctx context.Context, id string) (CronJob, error)
	ListCronJobs(ctx context.Context) ([]CronJob, error)
	PutCronJob(ctx context.Context, j CronJob) error
	DeleteCronJob(ctx context.Context, id string) error

	// --- Tenancy (0.3) ---

	CreateProject(ctx context.Context, p Project) error
	GetProject(ctx context.Context, name string) (Project, error)
	ListProjects(ctx context.Context) ([]Project, error)
	DeleteProject(ctx context.Context, name string) error

	CreateAPIKey(ctx context.Context, k APIKey) error
	// LookupAPIKey resolves a presented sha256 hash to the full key
	// (including project + scopes) and stamps LastUsedAt. Returns
	// ErrNotFound if the hash doesn't match any stored key.
	LookupAPIKey(ctx context.Context, tokenSHA256 []byte) (APIKey, error)
	ListAPIKeys(ctx context.Context, project string) ([]APIKey, error)
	DeleteAPIKey(ctx context.Context, id string) error

	SetQuota(ctx context.Context, project, kind string, value int64) error
	// GetQuota returns the configured limit. Unset kinds return 0
	// (interpreted by the gateway as unlimited) without an error.
	GetQuota(ctx context.Context, project, kind string) (int64, error)
	ListQuotas(ctx context.Context, project string) ([]Quota, error)

	// AddQuotaUsage increments the (project, kind, bucket) counter.
	// Bucket is typically time.Now().Truncate(time.Minute).
	AddQuotaUsage(ctx context.Context, project, kind string, bucket time.Time, delta int64) error
	// GetQuotaUsage sums all buckets at or after `since`.
	GetQuotaUsage(ctx context.Context, project, kind string, since time.Time) (int64, error)

	AppendAudit(ctx context.Context, e AuditEntry) error
	// ListAudit returns entries for `project` (or cluster-level if empty)
	// since the given time, newest first, capped at limit.
	ListAudit(ctx context.Context, project string, since time.Time, limit int) ([]AuditEntry, error)

	// Watch returns a channel of Events. Subscribers see every mutation
	// that happens through this Store (and, for Postgres, mutations
	// that arrive via LISTEN from peers). The channel is closed when
	// ctx is cancelled.
	Watch(ctx context.Context) (<-chan Event, error)

	// Close releases any resources (Postgres connections, goroutines).
	Close() error
}
