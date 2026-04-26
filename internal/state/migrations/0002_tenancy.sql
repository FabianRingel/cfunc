-- 0002_tenancy: projects, API keys, quotas, audit log.
--
-- Schema decisions:
--   * project name is the natural key (operators type it on the URL),
--     so we use TEXT PRIMARY KEY rather than a synthetic UUID. Renames
--     are not supported — pick the slug carefully.
--   * Functions/cron rows already had a project TEXT column from
--     0001_init; we add a foreign key now and ensure 'default' exists.
--   * api_keys store sha256(token) only — the plaintext is shown once
--     at creation. Lookup hashes the presented bearer and compares.
--   * Quotas are per (project, kind); aggregates are flushed from the
--     gateway every ~10s into quota_usage.
--   * Audit log is append-only; clients query by (project, ts) range.

CREATE TABLE IF NOT EXISTS cfunc_projects (
    name        TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO cfunc_projects (name, description)
    VALUES ('default', 'auto-created during 0002 migration')
    ON CONFLICT (name) DO NOTHING;

-- Backfill any pre-existing function/cron rows that still reference
-- a project that doesn't have a row yet (defensive — they should all
-- be 'default' since 0001).
INSERT INTO cfunc_projects (name, description)
    SELECT DISTINCT project, 'auto-created during 0002 migration'
    FROM cfunc_functions
    WHERE project NOT IN (SELECT name FROM cfunc_projects)
    ON CONFLICT DO NOTHING;
INSERT INTO cfunc_projects (name, description)
    SELECT DISTINCT project, 'auto-created during 0002 migration'
    FROM cfunc_cron_jobs
    WHERE project NOT IN (SELECT name FROM cfunc_projects)
    ON CONFLICT DO NOTHING;

ALTER TABLE cfunc_functions
    DROP CONSTRAINT IF EXISTS cfunc_functions_project_fkey;
ALTER TABLE cfunc_functions
    ADD CONSTRAINT cfunc_functions_project_fkey
    FOREIGN KEY (project) REFERENCES cfunc_projects(name) ON DELETE RESTRICT;

ALTER TABLE cfunc_cron_jobs
    DROP CONSTRAINT IF EXISTS cfunc_cron_jobs_project_fkey;
ALTER TABLE cfunc_cron_jobs
    ADD CONSTRAINT cfunc_cron_jobs_project_fkey
    FOREIGN KEY (project) REFERENCES cfunc_projects(name) ON DELETE RESTRICT;

-- API keys. The id is the user-visible identifier (e.g. "ck_a1b2c3");
-- token_sha256 is what we compare against. Scopes is a JSONB array of
-- {"admin","deploy","invoke"} entries.
CREATE TABLE IF NOT EXISTS cfunc_api_keys (
    id           TEXT PRIMARY KEY,
    project      TEXT NOT NULL REFERENCES cfunc_projects(name) ON DELETE CASCADE,
    description  TEXT NOT NULL DEFAULT '',
    token_sha256 BYTEA NOT NULL UNIQUE,
    scopes       JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS cfunc_api_keys_project_idx
    ON cfunc_api_keys (project);

-- Quotas: limits per project per resource kind. Kind is one of
-- ("requests_per_min", "ram_mb", "layer_bytes"). value is the limit;
-- 0 means unlimited (matches 0.2 behaviour).
CREATE TABLE IF NOT EXISTS cfunc_quotas (
    project    TEXT NOT NULL REFERENCES cfunc_projects(name) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    value      BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project, kind)
);

-- Aggregated usage. Gateways flush in-memory counters here every ~10s.
-- Bucket is the truncated-minute UTC timestamp; old rows can be pruned
-- by an external cron.
CREATE TABLE IF NOT EXISTS cfunc_quota_usage (
    project   TEXT NOT NULL REFERENCES cfunc_projects(name) ON DELETE CASCADE,
    kind      TEXT NOT NULL,
    bucket    TIMESTAMPTZ NOT NULL,
    value     BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (project, kind, bucket)
);

CREATE INDEX IF NOT EXISTS cfunc_quota_usage_recent_idx
    ON cfunc_quota_usage (project, kind, bucket DESC);

-- Audit log. Append-only; one row per state-changing admin action.
CREATE TABLE IF NOT EXISTS cfunc_audit_log (
    id        BIGSERIAL PRIMARY KEY,
    ts        TIMESTAMPTZ NOT NULL DEFAULT now(),
    project   TEXT,                      -- nullable: cluster-level events
    actor     TEXT NOT NULL,             -- api key id or "admin-token"
    action    TEXT NOT NULL,             -- e.g. "function.put"
    target    TEXT NOT NULL,             -- function name, key id, …
    metadata  JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS cfunc_audit_log_project_ts_idx
    ON cfunc_audit_log (project, ts DESC);
