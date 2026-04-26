# cfunc — Referenz für KI-Agenten

> Diese Datei ist als **single source of truth** für einen Coding-Agent
> gedacht, der eine cfunc-Function schreiben und gegen eine laufende
> Instanz deployen soll. Alles, was nötig ist, steht hier — keine
> externen Links müssen aufgelöst werden.

---

## 1. Was ist cfunc

cfunc ist ein selbstgehosteter FaaS-Runner. Eine Function ist ein
ausführbares Programm in Go, Python oder Node, das beim Start einen
Unix-Socket öffnet (Pfad in `CFUNC_SOCKET`) und sequentiell JSON-Frames
beantwortet. Der Gateway routet HTTP-Requests an `/fn/<name>` an die
Function und kümmert sich um Spawn / Pool / Idle-Reap.

## 2. Endpoints

| Methode | URL                              | Zweck                                 |
|---------|----------------------------------|---------------------------------------|
| GET     | `:8080/fn/<name>`                | Function aufrufen (öffentlich)        |
| POST    | `:8080/fn/<name>`                | dito                                  |
| POST    | `:8081/_/api/functions`          | Function registrieren/ersetzen        |
| DELETE  | `:8081/_/api/functions/<name>`   | Function entfernen                    |
| GET     | `:8081/_/api/state`              | Status-Snapshot                       |

Admin-Port (8081) bindet Default auf `127.0.0.1`. Bei exposed Bind ist ein
Token Pflicht (`Authorization: Bearer <token>` oder `?token=<token>`).

## 3. Function-Contract

**Eingabe** an den Handler:

```typescript
type Event = {
  method:  string                   // "GET" | "POST" | ...
  path:    string                   // "/fn/<name>"
  headers: Record<string, string>
  body:    unknown                  // JSON-Wert; bei nicht-JSON: String
}

type Context = {
  deadline_ms?: number
  trace_id?:    string
}
```

**Ausgabe**:

```typescript
type Response = {
  status:  number                   // HTTP-Statuscode
  headers: Record<string, string>
  body:    unknown                  // wird als JSON serialisiert
}
```

## 4. Handler-Templates (kopierbar)

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

`chmod +x handler.py`. SDK liegt unter `sdks/python/cfunc.py`. PYTHONPATH
setzen beim Register:

```json
{
  "name": "myfn",
  "binary": "/abs/path/to/handler.py",
  "env": ["PYTHONPATH=/abs/path/to/sdks/python:/abs/path/to/handler-dir"]
}
```

Wenn Deps via pip nötig: pip install --target=DIR und DIR ebenfalls in
PYTHONPATH.

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

`chmod +x handler.mjs`. Im selben Verzeichnis ein `package.json`:

```json
{
  "name": "myfn",
  "type": "module",
  "dependencies": { "cfunc": "file:/abs/path/to/sdks/node/cfunc" }
}
```

Dann **einmalig**:
```sh
cd <handler-dir> && npm install
```

Register (kein env nötig, weil node_modules nebenan liegt):
```json
{"name":"myfn","binary":"/abs/path/to/handler.mjs"}
```

## 5. Deployment-Recipe (vollständig)

```sh
# 1. Function bauen / vorbereiten (eine der drei Sprachen oben)

# 2. Registrieren
curl -X POST http://127.0.0.1:8081/_/api/functions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $CFUNC_ADMIN_TOKEN" \
  -d '{
    "name":   "myfn",
    "binary": "/abs/path/to/binary-or-script",
    "env":    ["KEY=VAL"],
    "max_concurrency": 4
  }'
# erwartet: 201 Created, Body {"name":"myfn","endpoint":"/fn/myfn"}

# 3. Aufrufen
curl -X POST http://127.0.0.1:8080/fn/myfn \
  -H 'Content-Type: application/json' \
  -d '{"hello":"world"}'

# 4. Entfernen
curl -X DELETE http://127.0.0.1:8081/_/api/functions/myfn \
  -H "Authorization: Bearer $CFUNC_ADMIN_TOKEN"
```

`Authorization`-Header weglassen, wenn Admin-Port auf 127.0.0.1 ohne Token läuft.

## 6. State-API auswerten

```sh
curl http://127.0.0.1:8081/_/api/state
```

Antwort (gekürzt):

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

Für Liveness-Probing einer eigenen Function: `running == true` UND
`errors == 0` (oder errors-Delta = 0 über zwei Snapshots).

## 7. Häufige Stolpersteine

| Symptom                                           | Ursache / Fix                                          |
|---------------------------------------------------|--------------------------------------------------------|
| 500 `spawn: timeout waiting for user process`     | Binary-Start scheitert vor Socket-Connect: PATH/Imports prüfen |
| Python: `ModuleNotFoundError: cfunc`              | `PYTHONPATH` enthält `sdks/python` nicht               |
| Node: `Cannot find package 'cfunc'`               | `npm install` im Handler-Dir vergessen                 |
| `function not found: X`                           | Vor Aufruf nicht registriert                           |
| 401 vom Admin-Port                                | Token fehlt; Bearer-Header oder `?token=` setzen       |
| Self-Recursion deadlocked                         | Synchroner Wait auf Children → Pool blockiert. Children **fire-and-forget** dispatchen, Parent kehrt sofort zurück |
| Counter zeigt nach 30 s plötzlich 0               | Pool wurde gereapt; Lifetime-Counter sind in Stats enthalten — sollte nicht passieren in aktueller Version |

## 8. Voll-Beispiel: Function bauen + deployen + testen

Ziel: Eine Python-Function `urlcheck`, die eine URL als JSON kriegt und
Status + Final-URL zurückgibt.

```sh
WORKDIR=/tmp/urlcheck
SDK=/Users/fabian/cfunc/sdks/python      # absoluter Pfad zum Python-SDK
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

# 4. Registrieren
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
# erwartete Antwort enthält "final_url":"https://example.org/"
```

## 9. Was ein Agent beachten muss

1. **Nie `binary` mit relativem Pfad** — Gateway expandiert nicht.
2. **Skripte müssen ausführbar sein** (`chmod +x`) und einen Shebang
   haben, sonst spawnt das Gateway sie nicht.
3. **Alle Pfade absolut** in `binary` und `env`.
4. **Idempotente Registers**: derselbe Name überschreibt — risikofrei
   re-deployen.
5. **Error-Frames** vom SDK haben `error.type`, `error.message`,
   `error.stack` — Gateway gibt 500 zurück, der Body enthält die Message.
6. **Response.body** wird vom Gateway als JSON ausgeliefert; keine eigene
   Serialisierung.
7. **Body-Lesen**: `event.body` ist bereits geparst falls Request-Body
   valides JSON war, sonst der Roh-String.

## 10. Validierung nach Deployment

```sh
# Function existiert?
curl -s http://127.0.0.1:8081/_/api/state \
  | jq '.functions[] | select(.name=="myfn")'

# Function antwortet?
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/fn/myfn

# Errors aufgetreten?
curl -s http://127.0.0.1:8081/_/api/state \
  | jq '.functions[] | select(.name=="myfn") | .errors'
```

Wenn `errors > 0`: in den Gateway-Logs (stderr oder Dashboard) nach
`invoke failed fn=myfn` suchen — dort steht die Exception.
