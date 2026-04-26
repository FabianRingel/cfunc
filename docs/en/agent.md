# cfunc — AI Agent Reference

> This file is intended as a **single source of truth** for a coding
> agent that needs to write a cfunc function and deploy it against a
> running instance. Everything you need is here — no external links to
> resolve.

---

## 1. What is cfunc

cfunc is a self-hosted FaaS runner. A function is an executable program
in Go, Python, or Node that opens a Unix socket on startup (path in
`CFUNC_SOCKET`) and answers JSON frames sequentially. The gateway routes
HTTP requests at `/fn/<name>` to the function and handles spawn / pool /
idle reap.

## 2. Endpoints

| Method  | URL                              | Purpose                              |
|---------|----------------------------------|--------------------------------------|
| GET     | `:8080/fn/<name>`                | Invoke function (public)             |
| POST    | `:8080/fn/<name>`                | same                                 |
| POST    | `:8081/_/api/functions`          | Register / replace function          |
| DELETE  | `:8081/_/api/functions/<name>`   | Remove function                      |
| GET     | `:8081/_/api/state`              | Status snapshot                      |

The admin port (8081) binds to `127.0.0.1` by default. When exposed, a
token is required (`Authorization: Bearer <token>` or `?token=<token>`).

## 3. Function contract

**Input** to the handler:

```typescript
type Event = {
  method:  string                   // "GET" | "POST" | ...
  path:    string                   // "/fn/<name>"
  headers: Record<string, string>
  body:    unknown                  // JSON value; non-JSON: string
}

type Context = {
  deadline_ms?: number
  trace_id?:    string
}
```

**Output**:

```typescript
type Response = {
  status:  number                   // HTTP status
  headers: Record<string, string>
  body:    unknown                  // JSON-serialized
}
```

## 4. Handler templates (copy-paste)

### Go

```go
// File: handler.go
package main

import (
    "context"
    "encoding/json"
    "log"
    cfunc "github.com/fabianringel/cfunc/sdks/go"
)

func handle(ctx context.Context, e cfunc.Event) (cfunc.Response, error) {
    body, _ := json.Marshal(map[string]any{
        "echo": string(e.Body),
        "path": e.Path,
    })
    return cfunc.Response{
        Status:  200,
        Headers: map[string]string{"Content-Type": "application/json"},
        Body:    body,
    }, nil
}

func main() { _ = cfunc.Start(handle) }
```

Build:
```sh
go build -o /tmp/myfn ./path/to/dir
```

Register:
```json
{"name":"myfn","binary":"/tmp/myfn"}
```

### Python

```python
#!/usr/bin/env python3
# File: handler.py
import cfunc

def handle(event: cfunc.Event, ctx: cfunc.Context) -> cfunc.Response:
    return cfunc.Response(
        status=200,
        headers={"Content-Type": "application/json"},
        body={"echo": event.body, "path": event.path},
    )

if __name__ == "__main__":
    cfunc.start(handle)
```

`chmod +x handler.py`. The SDK lives at `sdks/python/cfunc.py`. Set
PYTHONPATH at register time:

```json
{
  "name": "myfn",
  "binary": "/abs/path/to/handler.py",
  "env": ["PYTHONPATH=/abs/path/to/sdks/python:/abs/path/to/handler-dir"]
}
```

If pip deps are needed: `pip install --target=DIR ...` and add `DIR`
to `PYTHONPATH` too.

### Node (ESM)

```javascript
#!/usr/bin/env node
// File: handler.mjs
import { start, Response } from 'cfunc'

await start(async (event, ctx) => new Response({
  status: 200,
  headers: { 'Content-Type': 'application/json' },
  body: { echo: event.body, path: event.path },
}))
```

`chmod +x handler.mjs`. In the same directory create `package.json`:

```json
{
  "name": "myfn",
  "type": "module",
  "dependencies": { "cfunc": "file:/abs/path/to/sdks/node/cfunc" }
}
```

Then **once**:
```sh
cd <handler-dir> && npm install
```

Register (no env needed — `node_modules` lives next to the handler):
```json
{"name":"myfn","binary":"/abs/path/to/handler.mjs"}
```

## 5. Full deployment recipe

```sh
# 1. Build/prepare the function (any of the three languages above)

# 2. Register
curl -X POST http://127.0.0.1:8081/_/api/functions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $CFUNC_ADMIN_TOKEN" \
  -d '{
    "name":   "myfn",
    "binary": "/abs/path/to/binary-or-script",
    "env":    ["KEY=VAL"],
    "max_concurrency": 4
  }'
# expects: 201 Created, body {"name":"myfn","endpoint":"/fn/myfn"}

# 3. Invoke
curl -X POST http://127.0.0.1:8080/fn/myfn \
  -H 'Content-Type: application/json' \
  -d '{"hello":"world"}'

# 4. Remove
curl -X DELETE http://127.0.0.1:8081/_/api/functions/myfn \
  -H "Authorization: Bearer $CFUNC_ADMIN_TOKEN"
```

Drop the `Authorization` header if the admin port runs on `127.0.0.1`
without a token.

## 6. Reading the state API

```sh
curl http://127.0.0.1:8081/_/api/state
```

Response (truncated):

```json
{
  "started_at": "2026-04-26T10:00:00Z",
  "now":        "2026-04-26T10:01:23Z",
  "idle_ttl_ms": 30000,
  "functions": [
    {
      "name": "myfn",
      "endpoint": "/fn/myfn",
      "binary": "/path/...",
      "running": true,
      "pool_size": 2,
      "max_concurrency": 4,
      "mode": "process",
      "cold_start_ms": 35,
      "invokes": 17,
      "errors": 0,
      "avg_duration_ms": 4.2,
      "layers": [...]
    }
  ],
  "layers": [
    {"name":"shared@1.0","ref_count":3,"warm_refs":2,"references":["a","b","c"]}
  ]
}
```

For liveness probing of your own function: `running == true` AND the
errors delta over two snapshots is 0.

## 7. Common pitfalls

| Symptom                                           | Cause / fix                                            |
|---------------------------------------------------|--------------------------------------------------------|
| 500 `spawn: timeout waiting for user process`     | Binary fails before socket connect: check PATH/imports |
| Python: `ModuleNotFoundError: cfunc`              | `PYTHONPATH` doesn't include `sdks/python`             |
| Node: `Cannot find package 'cfunc'`               | Forgot `npm install` in the handler dir                |
| `function not found: X`                           | Not registered before invocation                       |
| 401 from admin port                               | Missing token; set Bearer header or `?token=`          |
| Self-recursion deadlocks                          | Synchronous wait on children → pool blocks. Dispatch children **fire-and-forget**, return parent immediately |
| Counter resets after 30 s                         | Pool reaped; lifetime counters are kept in current code |

## 8. Full example: build + deploy + test

Goal: a Python function `urlcheck` that takes a URL as JSON and returns
status + final URL.

```sh
WORKDIR=/tmp/urlcheck
SDK=/Users/fabian/cfunc/sdks/python      # absolute path to Python SDK
DEPS=/tmp/urlcheck-deps
GATEWAY_PUBLIC=http://127.0.0.1:8080
GATEWAY_ADMIN=http://127.0.0.1:8081

# 1. Workspace
mkdir -p "$WORKDIR" && cd "$WORKDIR"

# 2. Deps
mkdir -p "$DEPS"
python3 -m pip install --target="$DEPS" --quiet httpx

# 3. Handler
cat > handler.py <<'PY'
#!/usr/bin/env python3
import cfunc, httpx

def handle(event, ctx):
    body = event.body if isinstance(event.body, dict) else {}
    url = body.get("url")
    if not url:
        return cfunc.Response(status=400, body={"error": "missing url"})
    with httpx.Client(timeout=10, follow_redirects=True) as cl:
        r = cl.get(url)
    return cfunc.Response(status=200, body={
        "final_url": str(r.url),
        "status":    r.status_code,
        "size":      len(r.content),
    })

if __name__ == "__main__":
    cfunc.start(handle)
PY
chmod +x handler.py

# 4. Register
curl -s -X POST "$GATEWAY_ADMIN/_/api/functions" \
  -H 'Content-Type: application/json' \
  -d "{
    \"name\":\"urlcheck\",
    \"binary\":\"$WORKDIR/handler.py\",
    \"env\":[\"PYTHONPATH=$DEPS:$SDK:$WORKDIR\"]
  }"

# 5. Test
curl -s -X POST "$GATEWAY_PUBLIC/fn/urlcheck" \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.org"}'
# expected response contains "final_url":"https://example.org/"
```

## 9. Rules an agent must follow

1. **Never use a relative path in `binary`** — the gateway does not
   expand it.
2. **Scripts must be executable** (`chmod +x`) and have a shebang, or
   the gateway can't spawn them.
3. **All paths absolute** in `binary` and `env`.
4. **Re-register is idempotent**: same name overwrites — safe to redeploy.
5. **Error frames** from the SDK carry `error.type`, `error.message`,
   `error.stack` — gateway returns 500 with the message in the body.
6. **`Response.body`** is JSON-serialized by the gateway; don't pre-encode.
7. **Reading the body**: `event.body` is already parsed if the request
   body was valid JSON; otherwise the raw string.

## 10. Validation after deployment

```sh
# Function present?
curl -s http://127.0.0.1:8081/_/api/state \
  | jq '.functions[] | select(.name=="myfn")'

# Function responds?
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/fn/myfn

# Any errors?
curl -s http://127.0.0.1:8081/_/api/state \
  | jq '.functions[] | select(.name=="myfn") | .errors'
```

If `errors > 0`: search the gateway logs (stderr or dashboard) for
`invoke failed fn=myfn` — the exception is recorded there.
