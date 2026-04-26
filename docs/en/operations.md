# Operator Guide

> How to start, configure, secure, and monitor a cfunc stack. For
> **function authors** see [`developer.md`](./developer.md), for an
> **AI agent** see [`agent.md`](./agent.md).

## Overview

cfunc is a self-hosted FaaS runner. A request flows:

```
Client ─► Public port :8080
              /v1/<project>/fn/<name>   (multi-tenant)
              /fn/<name>                (legacy / default project)
              │
              ▼
         Gateway   project-check + quota-check before acquire
              │  spawns function instance on demand
              ▼
        Function process (subprocess or OCI container)
              │  wire protocol over Unix socket
              ▼
        User handler in Go / Python / Node
```

**Two listeners:**

| Port  | Default            | Content                                                  |
|-------|--------------------|----------------------------------------------------------|
| Public  | `:8080`           | `/v1/<project>/fn/<name>`, `/fn/<name>` (compat), `/healthz` |
| Admin   | `127.0.0.1:8081`  | `/_/` dashboard, `/_/api/*` admin API, `/_/ws`           |

The admin port binds to **loopback by default**. Exposing it externally
without a token will refuse to start.

For **multi-replica cluster mode** (Postgres-backed state, leader-elected
cron, S3-compatible layer distribution), see the
[Hetzner quickstart](./hetzner.md) and the [Helm chart](../../deploy/helm/cfunc/).

## Components

| Binary           | Purpose                                                              |
|------------------|----------------------------------------------------------------------|
| `cmd/gateway`    | HTTP frontend, spawner, pool management, dashboard                   |
| `cmd/scheduler`  | Cron daemon, calls functions on schedule via gateway HTTP            |
| `cmd/cfunc`      | CLI for layer management and cron jobs                               |

## Requirements

- **Go ≥ 1.26** (build)
- **Node ≥ 18** (dashboard build, Node functions)
- **Python ≥ 3.11** (Python functions)
- **Docker** or **runc** (container mode, optional)
- **Lima** (macOS dev VM for runc tests, optional)

## Build

```sh
make dashboard     # builds the React bundle (once or after UI changes)
make build         # builds all Go binaries
make test          # runs all Go tests
```

`internal/dashboard/web/dist/` is consumed by Go's embed directive; after
any UI change, run `make dashboard` or the binary will ship stale assets.

## Start (local)

```sh
go run ./cmd/gateway -addr=:8080 -admin-addr=127.0.0.1:8081
```

Dashboard at `http://localhost:8081/_/`, function calls at
`http://localhost:8080/fn/<name>`.

## Configuration flags

```
-addr             Public listen address (default :8080)
-admin-addr       Admin listen address (default 127.0.0.1:8081)
-dash             Dashboard URL prefix (default /_/, "" disables)
-admin-token      Literal admin token (prefer env/file)
-admin-token-file Path to file containing the token
-fn               Optional initial function name
-binary           Optional initial binary (with -fn)
```

Token sources, in this order:

1. `-admin-token-file <path>`
2. env var `CFUNC_ADMIN_TOKEN`
3. `-admin-token <value>` (visible in `ps`; only for local use)

## Security

| Configuration                              | Behavior                              |
|--------------------------------------------|---------------------------------------|
| `-admin-addr=127.0.0.1:*`, no token        | Runs open — loopback isolates         |
| `-admin-addr=0.0.0.0:*` without token      | Refuses to start with clear error     |
| `-admin-addr=*` with token                 | Bearer or `?token=` auth required     |

Token usage:

```sh
# Header (curl, fetch)
curl -H 'Authorization: Bearer SECRET' http://host:8081/_/api/state

# Query string (browser WebSocket — can't set headers)
http://host:8081/_/api/state?token=SECRET
```

Comparison is constant-time. Token rotation: swap the file, send SIGTERM,
restart.

## Deploy first function

```sh
# 1. Build the example
go build -o /tmp/example ./templates/go/example

# 2. Register via admin API
curl -X POST http://127.0.0.1:8081/_/api/functions \
  -H 'Content-Type: application/json' \
  -d '{"name":"hello","binary":"/tmp/example"}'

# 3. Call it
curl http://127.0.0.1:8080/fn/hello
# {"hello":"world","method":"GET","path":"/fn/hello"}
```

The first call cold-starts the instance; subsequent calls hit it warm.
After `IdleTTL` (default 30 s) of inactivity it is terminated.

## Admin API

| Method  | Path                          | Effect                            |
|---------|-------------------------------|-----------------------------------|
| GET     | `/_/api/state`                | Snapshot (functions + layers)     |
| POST    | `/_/api/functions`            | Register / replace function       |
| DELETE  | `/_/api/functions/<name>`     | Remove function, kill pool        |
| GET     | `/_/ws`                       | WebSocket: state + log stream     |

`POST /_/api/functions` body:

```json
{
  "name": "hello",
  "binary": "/abs/path/to/binary-or-script",
  "env": ["KEY=VAL", "OTHER=..."],
  "max_concurrency": 4,
  "layers": [
    {"name":"shared@1","host_path":"/var/lib/cfunc/layers/<sha>","mount_path":"/opt/layers/shared"}
  ]
}
```

Re-registering the same name gracefully closes the running pool and
applies the new definition.

## Dashboard

`http://<admin-host>:<admin-port>/_/`

- **KPIs**: req/s, err/s, warm fns, error rate (live WebSocket push)
- **Charts** (rolling 2 min): requests/sec, errors/sec, warm count, avg
  latency, top functions
- **Layers**: aggregated by host path → page-cache sharing visualization
- **Functions**: pool status table, click for details
- **Live logs**: WebSocket stream with filter, level selector, clear

When auth is enabled: token entered once per tab, stored in
`sessionStorage`.

## Logging

- Gateway logs structured via `log/slog` to stderr.
- Key events: `spawned` (with `cold_start_ms`, `mode`, `pool_size`),
  `invoke` (with `request_id`, `duration_ms`, `mode`), `invoke failed`,
  `cron fire`.
- Dashboard reads from a 1000-event ring buffer with live push.

## Cron / scheduler

A separate daemon `cmd/scheduler` or as a library in `internal/scheduler`.

```sh
# Add a job
cfunc cron add --id daily --schedule "0 9 * * *" --function reports

# List / remove
cfunc cron list
cfunc cron rm daily

# Daemon (loads $CFUNC_STORE/cron.json)
cfunc-scheduler -store=/var/lib/cfunc/cron.json -gateway=http://127.0.0.1:8080
```

`SIGHUP` reloads the job list without restart.

Storage: a JSON file. Persistent across restarts. Multi-host support
(SQLite swap-in) is open work.

## Layer builder (`cmd/builder`)

Layers are built **server-side**, not on the operator's workstation.
That closes the classic supply-chain vector (compromised dev machine
produces compromised layer) and enforces hash-pinning.

**Start the builder** (on a dedicated build host):

```sh
echo "long-random-secret" > /etc/cfunc/builder.token
chmod 600 /etc/cfunc/builder.token
cfunc-builder \
  -addr=127.0.0.1:9090 \
  -token-file=/etc/cfunc/builder.token \
  -allow-python="3.11,3.12" \
  -allow-index="https://pypi.org/simple"
```

**Wire the gateway to it:**

```sh
cfunc-gateway ... \
  -builder-url=http://127.0.0.1:9090 \
  -builder-token-file=/etc/cfunc/builder.token
```

**Build a layer via the CLI:**

`requirements.txt` must be **fully hash-pinned**. Anything else is
rejected with HTTP 400 before `pip` runs at all.

```sh
# requirements.txt:
#   numpy==1.26.0 \
#       --hash=sha256:abc...
#   beautifulsoup4==4.14.3 \
#       --hash=sha256:def...

cfunc layer build-python \
  --name pylib --version 1.0.0 \
  --requirements ./requirements.txt \
  --python 3.11 \
  --gateway http://127.0.0.1:8081 \
  --token-file /etc/cfunc/admin.token
```

Generate hash-pinned files with `pip-compile --generate-hashes` from
the `pip-tools` package.

## Layer store

Layers are **content-addressed directories** on the host, mounted
read-only into multiple function containers. Identical paths = identical
inode = one set of pages in the Linux page cache, shared across all
containers.

```sh
# Register a layer from an existing directory
cfunc layer add --name shared --version 1.0 \
  --mount /opt/layers/shared --from /path/to/content

# Build a Python layer from pip requirements
cfunc layer build-python --name pylib --version 1.0 \
  --requirements requirements.txt
```

Default store: `$CFUNC_STORE/layers/` (or `/var/lib/cfunc/layers/`).

A function references a layer at register time:

```json
"layers": [{"name":"pylib@1.0","host_path":"...","mount_path":"/opt/layers/pylib"}]
```

The dashboard's Layers panel shows reference counts per layer and the
effective dedup ratio.

### Layer distribution (S3-compatible)

For multi-host clusters, configure an S3-compatible backend so each
gateway can pull missing layers by their `sha256:` digest. Supported:
Hetzner Object Storage, RustFS (the actively-maintained MinIO drop-in,
Apache 2.0), AWS S3.

The gateway accepts a layer record with a `digest` field; on first
reference it pulls the gzipped tar from the configured backend, verifies
the content hash, and extracts under a host cache directory before
mounting the function container. Tar extraction is hardened against
path-traversal and symlink-slip attacks.

`internal/layerstore` holds the client; the gateway wires it up via
`Options.LayerResolver`. Builder-side push is the 0.4 deliverable —
0.3 ships the pull-on-first-reference path and the `Digest` field on
`LayerMount`.

## Multi-tenancy

cfunc 0.3+ scopes all resources by **project**. Functions, cron jobs,
API keys, quotas, and audit entries belong to exactly one project.
The `default` project is auto-created at migration time and is what
the legacy `/fn/<name>` route addresses; new projects use the
`/v1/<project>/fn/<name>` form.

### Bootstrap

Given a Postgres DSN:

```sh
DSN="postgres://cfunc:…@db/cfunc?sslmode=require"

cfunc cluster init --dsn "$DSN"

# Create a project
cfunc project create --dsn "$DSN" --name acme --description "ACME team"
cfunc project list   --dsn "$DSN"
```

### API keys

Keys carry a comma-separated set of scopes:

| Scope   | Meaning                                                           |
|---------|-------------------------------------------------------------------|
| `admin` | Manage the project (create/revoke keys, set quotas) — implies the others |
| `deploy`| Register or unregister functions / cron jobs in this project       |
| `invoke`| Call functions of this project at the public route                |

```sh
cfunc key create --dsn "$DSN" --project acme --scopes deploy,invoke
# id:    ck_…
# token: ck_….<plaintext>          ← shown ONCE — store it now
# (plaintext token shown once — store it now)

cfunc key list   --dsn "$DSN" --project acme
cfunc key revoke --dsn "$DSN" --id ck_…
```

The token is only ever stored as `sha256(plaintext)`. Lookup is
constant-time at the bearer-comparison layer; the index hit on the
hash is negligibly data-dependent.

A key with `project = "*"` is **rejected at creation** — that value is
the in-memory sentinel for cluster-admin identity (the legacy single
admin token), not a tenant scope.

### Quotas

Per-project, per-kind limits. Currently enforced: `requests_per_min`.

```sh
cfunc quota set  --dsn "$DSN" --project acme --kind requests_per_min --value 1000
cfunc quota list --dsn "$DSN" --project acme
```

A value of `0` means unlimited (the default for unset projects).

Enforcement is **approximately cluster-wide**: every gateway maintains
an in-memory token bucket and flushes its delta into Postgres every
~10 seconds, then refreshes the cluster aggregate. Under heavy parallel
load each gateway can independently overshoot by up to one bucket-window
worth of requests before the next sync. The trade-off buys a per-request
hot path with no DB hit. Strict-cap enforcement is not on the 1.0
roadmap; if you need hard limits, set the value below the soft target.

### Audit log

Every state-changing CLI / admin-API call appends a row to
`cfunc_audit_log`:

```sh
cfunc audit tail --dsn "$DSN" --project acme           # project events
cfunc audit tail --dsn "$DSN"                          # cluster-level events
cfunc audit tail --dsn "$DSN" --project acme --limit 200
```

Recorded actions: `function.put`, `function.delete`, `project.create`,
`project.delete`, `key.create`, `key.revoke`, `quota.set`. Entries
include `actor` (`admin-token`, the `ck_…` key id, or `cli`), `target`,
and a JSONB `metadata` blob.

The `cfunc_audit_log` table is append-only and never truncated by
cfunc itself. Operators who need retention limits should add a
periodic `DELETE … WHERE ts < now() - interval '90 days'` cron.

## Container mode (Linux + runc)

On Linux with `runc` installed:

- `internal/spawn.StartRunc` builds an OCI bundle with a read-only
  rootfs, layer + socket bind-mounts, and the standard Linux namespaces.
- Default capability set matches Docker.
- macOS development: VM via `make lima-up` (Lima + Ubuntu + runc + Go).

The spawner choice (subprocess vs runc) is wired in the `cmd/gateway`
setup. For Linux production, set `gateway.Options.Spawn` to a
`StartRunc`-backed closure.

## TLS / ACME

cfunc can negotiate and renew Let's Encrypt certs itself (via
`certmagic` + libdns) — no external reverse proxy required.

**Public port with HTTP-01 (single host, no wildcards):**

```sh
cfunc-gateway \
  -addr=:443 \
  -tls-domain=fn.example.org \
  -tls-email=ops@example.org
# Port 80 must be reachable for the ACME challenge.
```

**Public port with DNS-01 (wildcards, no port-80 dependency):**

```sh
HETZNER_DNS_API_TOKEN=… cfunc-gateway \
  -addr=:443 \
  -tls-domain="fn.example.org,*.fn.example.org" \
  -tls-email=ops@example.org \
  -tls-dns-provider=hetzner
```

**Bundled providers:**

| Provider | Env vars |
|---|---|
| `cloudflare` | `CF_API_TOKEN` (Zone:Read + DNS:Edit) |
| `hetzner` | `HETZNER_DNS_API_TOKEN` (DNS-console token, NOT Cloud API) |
| `route53` | AWS standard chain (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`) |
| `digitalocean` | `DO_AUTH_TOKEN` |
| `rfc2136` | `RFC2136_SERVER`, `RFC2136_KEY_NAME`, `RFC2136_KEY_ALG`, `RFC2136_KEY` |

**Admin port with its own cert:**

```sh
cfunc-gateway \
  -admin-addr=:8443 \
  -admin-tls-domain=admin.example.org \
  -tls-email=ops@example.org \
  -tls-dns-provider=hetzner \
  -admin-token-file=/etc/cfunc/admin.token
```

**Additional flags:**

| Flag | Purpose |
|---|---|
| `-tls-storage <path>` | cert cache directory (default: certmagic standard) |
| `-tls-staging` | use Let's Encrypt staging (testing — avoids rate limits) |
| `-tls-http-addr` | port for HTTP-01 challenges + HTTP→HTTPS redirect (default `:80`) |

**First-time setup:**

```sh
# Stage first to avoid LE production rate limits:
cfunc-gateway -tls-domain fn.test ... -tls-staging
# Once it succeeds, remove the flag for real certs.
```

Renewal happens in the background; no restart needed. Cert files live
in `tls-storage`, protected by file locks — multiple gateway instances
can safely share the same storage.

## Health check

```sh
curl http://localhost:8080/healthz   # 200 ok
```

The admin port intentionally has no token-free health endpoint — point
load balancer probes at the public port.

## Troubleshooting

| Symptom                                    | Diagnosis / fix                                   |
|--------------------------------------------|---------------------------------------------------|
| `spawn: timeout waiting for user process`  | Binary can't find its deps; check PATH/PYTHONPATH/`node_modules` |
| `function not found: X`                    | Not registered or forgotten after restart        |
| Dashboard shows nothing, WS disconnected   | Token missing or wrong; check browser console    |
| `pool=N/M` with M < N                      | Race in old version; upgrade                     |
| Counter resets to 0                        | Pool was reaped; lifetime counter in stats since pool refactor |
| `runc run failed: ... permission denied`   | Rootfs/mount source not traversable; mode 0755   |
| `runc run failed: rootless ... user namespaces` | Spec needs user-NS or run as root           |

For deeper analysis: filter dashboard logs, or read `slog` directly
(`stderr` of the gateway process).

## Stop the stack

```sh
pkill -TERM cfunc-gateway
pkill -TERM cfunc-scheduler
docker rm -f cfunc-pg     # if Postgres demo is up
```

Functions die with the gateway (pool is shut down on Close).
