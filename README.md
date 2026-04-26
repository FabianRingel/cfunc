# cfunc

> **Self-hosted Function-as-a-Service for the European cloud.**
> Single-binary, EU-jurisdiction by default, Lambda-compatible enough.

cfunc is an open-source FaaS runner you deploy on your own
infrastructure — Hetzner, Scaleway, OVH, on-prem. Functions are written
in Go, Python, or Node, isolated in OCI containers, and scaled to zero
between calls. Page-cache-shared dependency layers make multi-function
deployments memory-efficient without orchestrator overhead.

> **Status:** 0.2 — cluster-ready code landed. Multi-replica gateways
> share state via Postgres (`LISTEN/NOTIFY`), cron has leader election
> via `pg_try_advisory_lock`, and `internal/layerstore` provides an
> S3-compatible layer-distribution backend. Helm chart and
> docker-compose ship with 0.2.1. See [`STRATEGY.md`](./STRATEGY.md)
> for the roadmap.

```sh
# A Go handler, deployed and called in 4 commands:
go build -o /tmp/hello ./templates/go/example
go run  ./cmd/gateway &
curl -X POST http://127.0.0.1:8081/_/api/functions \
  -H 'Content-Type: application/json' \
  -d '{"name":"hello","binary":"/tmp/hello"}'
curl http://127.0.0.1:8080/fn/hello
# {"hello":"world","method":"GET","path":"/fn/hello"}
```

---

## Why does this exist?

There is no European hyperscaler-equivalent for serverless functions.
AWS Lambda, Cloud Run, and Azure Functions are great products — and they
all run outside EU jurisdiction. For data-sensitive workloads under
GDPR, that's a problem.

cfunc fills the gap with a self-hosted alternative that:

- runs on any Linux host or VM (Hetzner is the reference target)
- has Lambda-shaped semantics (HTTP triggers, cron, scale-to-zero)
- ships as a small set of static Go binaries plus a React dashboard
- is permissively licensed (Apache 2.0) so you can build a commercial
  managed service on top

It does not try to be Kubernetes, OpenFaaS, or Knative. It deliberately
avoids requiring an orchestrator, because the deployment story we care
about is "three Hetzner servers and Postgres".

## Features

| | |
|---|---|
| **Multi-language** | Go, Python, Node ESM. SDKs are ~150 LOC each, identical wire protocol. |
| **Container isolation** | OCI bundles via `runc` on Linux. Subprocess fallback for macOS dev. |
| **Page-cache layer sharing** | Bind-mount layer dirs are inode-deduplicated; 30 functions sharing `numpy` use one set of pages in RAM. |
| **Server-side layer builds** | `cfunc-builder` daemon runs `pip install --require-hashes` in a sandbox. The operator's workstation never produces layer content. |
| **Scale-to-zero per function** | Cold-start ~30 ms (Go runc), ~35 ms (Node), ~300 ms (Python). Per-function pool with `MaxConcurrency`. |
| **Embedded dashboard** | React + WebSocket-streamed metrics, served by the gateway binary. |
| **ACME / Let's Encrypt** | Built-in via `certmagic`. HTTP-01 and DNS-01, with libdns providers for Cloudflare, Hetzner, Route53, DigitalOcean, RFC2136. |
| **Cron** | In-process scheduler with Postgres-backed leader election (`pg_try_advisory_lock`); JSON-persisted in single-node mode. |
| **Cluster mode** | Multi-replica gateways share state via Postgres `LISTEN/NOTIFY`. `cfunc cluster init/status` for setup. Single-node deployments stay zero-config. |
| **Layer distribution** | `internal/layerstore` with S3-compatible backend (Hetzner Object Storage, RustFS, AWS S3); `Noop` default for single-node. |
| **Hardened by default** | Token-auth on admin port, loopback default, body-size caps, SSRF block on the scraper template, sanitized subprocess env (admin token never leaks to functions), function-name allow-list, request timeouts. |

Sustained throughput on a single M-series macOS dev box: **~18 500
req/s, 0 errors**, with the gateway at ~1.5 cores. Cold-starts under
50 ms for compiled languages.

## Architecture

```
              public                                 admin (loopback or token)
              :8080                                  :8081
              /fn/<name>                             /_/   dashboard
                │                                    /_/api/* admin API
                ▼                                    /_/ws    live metrics
        ┌───────────────────┐                              │
        │ cfunc-gateway     │◄──── Postgres (cluster state)│
        │  - HTTP frontend  │                              │
        │  - per-fn pools   │                              │
        │  - layer mounts   │       ┌──────────────────┐   │
        └─────────┬─────────┘       │  cfunc-builder   │◄──┘
                  │                 │  pip --require-  │
        OCI bundle│ via runc        │  hashes sandbox  │
                  ▼                 └──────────────────┘
        ┌───────────────────┐
        │ Function process  │  Go binary, Python script,
        │  + cfunc SDK      │  or Node ESM module — same
        │  ↕ Unix socket    │  length-prefixed JSON wire
        │  (length-prefix   │  protocol regardless of language.
        │   JSON frames)    │
        └───────────────────┘
```

Per-function pools keep `MaxConcurrency` warm instances. When all are
busy, additional requests block on `sync.Cond` until a slot frees —
fair-share across all waiters, not first-come.

Layer storage is content-addressed (`sha256` over the file tree). The
gateway bind-mounts layer dirs read-only into containers; identical
host paths share the kernel page cache across all functions referencing
them on the same host.

The builder is a separate daemon for security: layer creation needs
`pip` and network access, gateway hosts shouldn't. They communicate
over HTTP with a shared bearer token.

## Quickstart

### macOS / Linux dev

```sh
git clone https://github.com/fabianringel/cfunc
cd cfunc
make dashboard         # build the React bundle (one-off)
make build             # build all Go binaries
make test              # run all tests under -race
```

### Run locally

```sh
# Terminal 1 — gateway
go run ./cmd/gateway

# Terminal 2 — write and deploy a Go handler
cat > /tmp/myfn.go <<'GO'
package main
import (
    "context"
    cfunc "github.com/fabianringel/cfunc/sdks/go"
)
func main() {
    cfunc.Start(func(_ context.Context, e cfunc.Event) (cfunc.Response, error) {
        return cfunc.Response{Status: 200, Body: []byte(`{"hello":"world"}`)}, nil
    })
}
GO

go build -o /tmp/myfn /tmp/myfn.go
curl -X POST http://127.0.0.1:8081/_/api/functions \
  -H 'Content-Type: application/json' \
  -d '{"name":"hello","binary":"/tmp/myfn"}'

curl http://127.0.0.1:8080/fn/hello
```

Open `http://127.0.0.1:8081/_/` for the dashboard.

### Linux container mode (production-like)

`runc` must be installed. The gateway will spawn each function in its
own OCI bundle with a read-only rootfs, bind-mounted layers, and
standard Linux namespaces. macOS users can develop against this via
Lima — `make lima-up && make test-runc`.

### Production with TLS

```sh
# DNS-01 wildcard cert via Hetzner DNS, no port-80 dependency
HETZNER_DNS_API_TOKEN=… cfunc-gateway \
  -addr=:443 \
  -tls-domain="fn.example.org,*.fn.example.org" \
  -tls-email=ops@example.org \
  -tls-dns-provider=hetzner \
  -admin-addr=127.0.0.1:8081 \
  -admin-token-file=/etc/cfunc/admin.token
```

## Writing a function

Three SDKs, identical handler shape:

<details>
<summary><b>Go</b></summary>

```go
package main

import (
    "context"
    "encoding/json"
    cfunc "github.com/fabianringel/cfunc/sdks/go"
)

func handle(_ context.Context, e cfunc.Event) (cfunc.Response, error) {
    body, _ := json.Marshal(map[string]string{"hello": "go", "path": e.Path})
    return cfunc.Response{
        Status:  200,
        Headers: map[string]string{"Content-Type": "application/json"},
        Body:    body,
    }, nil
}

func main() { cfunc.Start(handle) }
```
</details>

<details>
<summary><b>Python</b></summary>

```python
#!/usr/bin/env python3
import cfunc

def handle(event: cfunc.Event, ctx: cfunc.Context) -> cfunc.Response:
    return cfunc.Response(status=200, body={"hello": "py", "path": event.path})

if __name__ == "__main__":
    cfunc.start(handle)
```
</details>

<details>
<summary><b>Node (ESM)</b></summary>

```javascript
#!/usr/bin/env node
import { start, Response } from 'cfunc'

await start(async (event) => new Response({
    status: 200,
    body: { hello: 'node', path: event.path },
}))
```
</details>

Full developer guide: [`docs/en/developer.md`](./docs/en/developer.md).

## Comparison

| | cfunc | OpenFaaS | Knative | AWS Lambda |
|---|---|---|---|---|
| Self-hosted | ✅ | ✅ | ✅ | ❌ |
| Requires Kubernetes | ❌ | ✅ | ✅ | n/a |
| Single-binary install | ✅ | ❌ | ❌ | n/a |
| EU jurisdiction by default | ✅ | depends on host | depends on host | ❌ |
| Multi-tenant primitives | planned (0.3) | ✅ | ✅ | ✅ |
| Built-in dashboard | ✅ | ✅ | ❌ | ✅ |
| Lambda-shaped triggers | HTTP, cron | HTTP, queues | HTTP, eventing | every kind |
| License | Apache 2.0 | MIT | Apache 2.0 | proprietary |

cfunc deliberately stays smaller than OpenFaaS or Knative. It targets
the case where you want serverless functions but don't want a
Kubernetes operations team.

## Documentation

- [`docs/en/operations.md`](./docs/en/operations.md) — operator guide
  (deploy, configure, secure, monitor)
- [`docs/en/developer.md`](./docs/en/developer.md) — function-author
  guide (SDKs, deployment, layers)
- [`docs/en/agent.md`](./docs/en/agent.md) — single-page reference for
  AI agents writing/deploying functions
- [`docs/de/`](./docs/de/) — German versions of all of the above
- [`PLAN.md`](./PLAN.md) — phase-by-phase development log
- [`STRATEGY.md`](./STRATEGY.md) — vision, release roadmap, open design
  questions

## Project status & roadmap

| Release | Highlights | Status |
|---|---|---|
| **0.1** | Single-node, single-tenant, all SDKs, dashboard, TLS, builder | ✅ shipped |
| **0.2** | Cluster mode: Postgres state with `LISTEN/NOTIFY`, leader-elected cron, S3-compatible layerstore package | ✅ shipped |
| 0.2.1 | Helm chart, docker-compose stack, Hetzner quickstart docs, layerstore wired into builder/gateway | next |
| 0.3 | Multi-tenancy: projects, API keys with scopes, quotas, audit log | planned |
| 0.4 | Sticky routing, cold-start optimisation, pre-warming | planned |
| 0.5 | Lambda-parity triggers: API-gateway routes, queue triggers, S3-event triggers | planned |
| 0.6 | Operator suite: Terraform module for Hetzner, Helm chart, Prometheus exporter | planned |
| 0.7 | Hardening: layer signatures (cosign), policy engine, user-namespace per function | planned |
| 1.0 | Production-ready: full benchmarks, migration guide from Lambda | planned |

The full release plan with effort estimates lives in
[`STRATEGY.md`](./STRATEGY.md).

## Contributing

Contributions are welcome. The project is small enough that the easiest
way to discuss bigger changes is to open an issue first; small fixes
can go straight to a PR.

Please read [`STRATEGY.md`](./STRATEGY.md) for the project's positioning
before proposing significant features — cfunc deliberately stays
narrower than OpenFaaS or Knative, and "make it bigger" PRs may be
declined on scope grounds.

A formal `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, and
`SECURITY.md` will land alongside the 0.5 release. Until then:

- All contributions are accepted under the project's Apache 2.0
  license (Apache 2.0 §5 — submitting a contribution implies licensing
  it under the same terms)
- For security issues, please email the maintainer rather than opening
  a public issue (contact in `git log`)

## License

Apache License 2.0 — see [`LICENSE`](./LICENSE) for full text and
[`NOTICE`](./NOTICE) for third-party attributions.

You may use, modify, and redistribute cfunc — including running a
managed service or commercial distribution on top of it. Patent grant
included (Apache 2.0 §3). The "cfunc" name and logo are not granted as
trademarks (Apache 2.0 §6); a separate trademark policy will accompany
the 1.0 release.
