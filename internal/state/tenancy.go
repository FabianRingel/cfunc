// SPDX-License-Identifier: Apache-2.0

package state

import "time"

// Project is a tenant boundary. Functions, cron jobs, API keys, and
// quotas are all scoped by project name. The 'default' project is
// auto-created during migration 0002 and cannot be deleted while it
// holds resources.
type Project struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

// APIKey is the auth credential for project-scoped admin API calls.
// Plaintext is shown to the operator once at creation; the store only
// keeps the sha256 hash. Scopes is a subset of {"admin","deploy","invoke"}.
type APIKey struct {
	ID          string     `json:"id"`
	Project     string     `json:"project"`
	Description string     `json:"description,omitempty"`
	TokenSHA256 []byte     `json:"-"`
	Scopes      []string   `json:"scopes,omitempty"`
	CreatedAt   time.Time  `json:"created_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

// Quota is a single per-project limit. Kind is one of:
//   - "requests_per_min": invocation rate cap
//   - "ram_mb":           total resident pool memory
//   - "layer_bytes":      cumulative layer storage
//
// Value 0 means unlimited.
type Quota struct {
	Project   string    `json:"project"`
	Kind      string    `json:"kind"`
	Value     int64     `json:"value"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// AuditEntry is one append-only record of a state-changing action.
// Project is empty for cluster-level events (e.g. project.create).
type AuditEntry struct {
	ID       int64             `json:"id,omitempty"`
	TS       time.Time         `json:"ts,omitempty"`
	Project  string            `json:"project,omitempty"`
	Actor    string            `json:"actor"`
	Action   string            `json:"action"`
	Target   string            `json:"target"`
	Metadata map[string]string `json:"metadata,omitempty"`
}
