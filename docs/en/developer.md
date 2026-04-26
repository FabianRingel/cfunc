# Developer Guide

> How to write a cfunc function in Go, Python, or Node, test it locally,
> and deploy it. For **operator topics** see
> [`operations.md`](./operations.md).

## Function model

A function is an **executable program** that opens a Unix socket on
startup and answers requests sequentially. Per request it returns a
`Response` with status, headers, and body. The gateway spawns it on
demand (cold start), keeps it warm until idle TTL, then kills it.

**Fundamental:** one instance = one process serving **one** request at a
time. Concurrency comes from multiple pool instances (gateway default 4
per function).

## Handler contract (cross-language)

Input (`Event`):

```json
{
  "method":  "POST",
  "path":    "/fn/<name>",
  "headers": {"Content-Type": "application/json", "...": "..."},
  "body":    "<JSON value or string>"
}
```

Output (`Response`):

```json
{
  "status":  200,
  "headers": {"Content-Type": "application/json"},
  "body":    "<JSON-serializable value>"
}
```

`Context` carries an optional `deadline_ms` and a `trace_id`.

## Go

```go
package main

import (
    "context"
    "encoding/json"
    "log"

    cfunc "github.com/fabianringel/cfunc/sdks/go"
)

func handle(ctx context.Context, e cfunc.Event) (cfunc.Response, error) {
    body, _ := json.Marshal(map[string]string{"hello": "go", "path": e.Path})
    return cfunc.Response{
        Status:  200,
        Headers: map[string]string{"Content-Type": "application/json"},
        Body:    body,
    }, nil
}

func main() {
    if err := cfunc.Start(handle); err != nil {
        log.Fatal(err)
    }
}
```

Build:

```sh
go build -o /tmp/myfn ./mypath
```

Statically linked, runs in any cfunc mode including an empty container
rootfs.

## Python

```python
#!/usr/bin/env python3
import cfunc

def handle(event: cfunc.Event, ctx: cfunc.Context) -> cfunc.Response:
    return cfunc.Response(
        status=200,
        headers={"Content-Type": "application/json"},
        body={"hello": "py", "path": event.path},
    )

if __name__ == "__main__":
    cfunc.start(handle)
```

Make the script executable, expose the SDK via `PYTHONPATH`:

```sh
chmod +x handler.py
# at register time:
"env": ["PYTHONPATH=/path/to/sdks/python:/path/to/handler-dir"]
```

For dependencies: `cfunc layer build-python --requirements req.txt`
builds a layer; add its `host_path` to `PYTHONPATH` too.

## Node (ESM)

```javascript
#!/usr/bin/env node
import { start, Response } from 'cfunc'

await start(async (event, ctx) => new Response({
  status: 200,
  headers: { 'Content-Type': 'application/json' },
  body: { hello: 'node', path: event.path },
}))
```

In the function's directory create a `package.json`:

```json
{
  "type": "module",
  "dependencies": { "cfunc": "file:/path/to/sdks/node/cfunc" }
}
```

then run `npm install` once.

NODE_PATH does **not** work for ESM — the resolver requires a real
`node_modules/cfunc`, which `npm install` with the `file:` dep creates.

## Using layers

Layers are **read-only directories** mounted identically into multiple
function containers. Use them for:

- pip / npm dependencies
- ML models
- large static assets (fonts, tokenizer vocabs)
- shared config

Register a layer:

```sh
cfunc layer add --name fonts --version 1.0 \
  --mount /opt/layers/fonts --from ./fonts/
```

Function manifest or admin API references it:

```json
"layers": [
  {"name":"fonts@1.0","host_path":"/var/lib/cfunc/layers/<sha>","mount_path":"/opt/layers/fonts"}
]
```

Effect: when 30 functions reference the same layer, the Linux page cache
holds **one** copy of the bytes for all concurrently-running containers.

## Function manifest (`cfunc.yaml`)

Optional, useful for local development and deployment tooling:

```yaml
name: my-fn
runtime: python-3
binary: ./handler.py
layers:
  - shared-config@1.0
  - pylib@2.0
```

`internal/manifest.Load(path)` parses and resolves the binary path
relative to the manifest.

## Deployment

**Static (startup):**

```sh
cfunc-gateway -fn=hello -binary=/tmp/example
```

One function registered at boot.

**Dynamic (runtime):**

```sh
curl -X POST http://localhost:8081/_/api/functions \
  -H 'Authorization: Bearer SECRET' \
  -H 'Content-Type: application/json' \
  -d @function.json
```

`function.json`:

```json
{
  "name": "my-fn",
  "binary": "/abs/path/handler",
  "env": ["PYTHONPATH=...","CFUNC_GATEWAY_URL=http://localhost:8080"],
  "max_concurrency": 8,
  "layers": [{...}]
}
```

Re-registering the same name replaces the definition and gracefully
closes the running pool.

### Multi-tenant deployments

In cluster mode, every function belongs to a **project**. Pass the
project at register time; if omitted, it's set to `default`:

```json
{
  "name": "my-fn",
  "binary": "/abs/path/handler",
  "project": "acme"
}
```

Invoke at the project-scoped route: `/v1/acme/fn/my-fn`. The legacy
`/fn/my-fn` URL still works for the `default` project but is rejected
for any other project.

API-key bearers must hold the `deploy` scope to register and `invoke`
to call. See [`operations.md`](./operations.md) for the full key /
quota / audit story.

## Concurrency and recursion

Each function has an **instance pool**. `max_concurrency` caps its size
(default 4). When the pool is full, additional requests block on the
oldest slot.

**Recursive functions** (function calls itself via `/fn/<self>`):

- Synchronous wait would deadlock the pool (parent holds slot, child
  waits for slot).
- Solution: **fire-and-forget** — parent dispatches children and returns
  immediately. See `templates/python/scraper/scrape.py` for an example.
- Size the pool generously (`max_concurrency: 12+`) when fan-out is high.

## Scheduler (cron)

To trigger a function periodically:

```sh
cfunc cron add --id daily --schedule "0 9 * * *" --function reports
```

Schedule format: 5-field standard cron (`min hour dom mon dow`) or
`@every 30s` / `@hourly` / `@daily`.

The scheduler daemon calls `/fn/<name>` at the appointed time, so the
normal spawn-on-demand path applies — no special function variant needed.

## Local testing

**Calling the script directly** without a gateway won't work
(`CFUNC_SOCKET` is missing). Instead:

1. Start the gateway locally
2. Register the function via admin API
3. `curl` against `/fn/<name>`

For unit-testing handler code: in Go call the handler function directly
with a synthesized `cfunc.Event`/`cfunc.Response`. Likewise Python and
Node.

The SDKs themselves have test suites:

```sh
go test ./sdks/go/...
python3 -m unittest sdks/python/test_cfunc
node --test sdks/node/cfunc/test_cfunc.mjs
```

## Wire protocol (for SDK implementers)

Frame: `[4 bytes BE length N][N bytes JSON]`. `N <= 16 MiB`.

Frame types:

| Type           | Direction | Purpose                              |
|----------------|-----------|--------------------------------------|
| `init`         | → fn      | optional setup, reply `init_ok`      |
| `invoke`       | → fn      | handler call, reply `result`/`error` |
| `result`       | ← fn      | success                              |
| `error`        | ← fn      | exception, includes stack trace      |
| `shutdown`     | → fn      | graceful stop, reply `shutdown_ok`   |

Sequential per connection. One invoke at a time per instance.

Reference implementations: `internal/wire` (Go), `sdks/python/cfunc.py`,
`sdks/node/cfunc/index.mjs`.

## Best practices

- **Idempotency**: functions should tolerate repeated calls (the
  scheduler doesn't auto-retry on failure, but fire-and-forget recursion
  can produce duplicates).
- **Statefulness**: don't assume anything survives a cold start.
  In-process caches (e.g., an ML model) are fine but go away after idle
  reap.
- **Side effects**: open DB connections per invoke or via a connection
  pool — don't take global locks without cleanup.
- **Error handling**: panics are caught by the SDK → error frame with
  stack trace. HTTP status: 500. For 4xx errors return an explicit
  `Response{Status: 4xx}` rather than raising.
- **Logs**: stdout/stderr are forwarded by the gateway and surface in
  the gateway's logs.
