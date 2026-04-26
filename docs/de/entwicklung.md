# Entwicklerdokumentation

> Wie man eine cfunc-Function in Go, Python oder Node schreibt, lokal
> testet und deployt. Für **Betreiber-Themen** siehe
> [`betrieb.md`](./betrieb.md).

## Function-Modell

Eine Function ist ein **ausführbares Programm**, das beim Start einen
Unix-Socket öffnet und Anfragen sequentiell beantwortet. Pro Anfrage
liefert sie eine `Response` mit Status, Headers und Body. Der Gateway
spawnt die Function on demand (Cold-Start), hält sie warm bis Idle-TTL,
killt sie dann.

**Fundamental:** eine Instance = ein Prozess, der **einen** Request zur
Zeit bearbeitet. Concurrency entsteht durch mehrere Pool-Instanzen
(Gateway-Default 4 pro Function).

## Handler-Contract (sprach-übergreifend)

Eingabe (`Event`):

```json
{
  "method":  "POST",
  "path":    "/fn/<name>",
  "headers": {"Content-Type": "application/json", "...": "..."},
  "body":    "<entweder JSON-Wert oder String>"
}
```

Ausgabe (`Response`):

```json
{
  "status":  200,
  "headers": {"Content-Type": "application/json"},
  "body":    "<JSON-serialisierbarer Wert>"
}
```

`Context` enthält ein optionales `deadline_ms` und eine `trace_id`.

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

Statisch gelinkt, läuft in jedem cfunc-Modus inkl. leerem Container-Rootfs.

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

Skript ausführbar machen, SDK über `PYTHONPATH` reinhängen:

```sh
chmod +x handler.py
# beim Register:
"env": ["PYTHONPATH=/path/to/sdks/python:/path/to/handler-dir"]
```

Bei Deps: `cfunc layer build-python --requirements req.txt` baut einen
Layer; `host_path` davon zusätzlich in `PYTHONPATH` legen.

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

Im Function-Verzeichnis ein `package.json` mit:

```json
{
  "type": "module",
  "dependencies": { "cfunc": "file:/path/to/sdks/node/cfunc" }
}
```

und einmalig `npm install`.

NODE_PATH funktioniert für ESM **nicht** — node_modules-Resolution braucht
echtes `node_modules/cfunc`, das `npm install` mit `file:`-Dep einrichtet.

## Layers nutzen

Layers sind **read-only Verzeichnisse**, die identisch in mehrere
Function-Container gemountet werden. Anwendungsfälle:

- pip/npm-Dependencies
- ML-Modelle
- große statische Assets (Fonts, Tokenizer-Vocabs)
- Shared Config

Layer registrieren:

```sh
cfunc layer add --name fonts --version 1.0 \
  --mount /opt/layers/fonts --from ./fonts/
```

Function-Manifest oder Admin-API referenziert ihn:

```json
"layers": [
  {"name":"fonts@1.0","host_path":"/var/lib/cfunc/layers/<sha>","mount_path":"/opt/layers/fonts"}
]
```

Effekt: Wenn 30 Functions denselben Layer referenzieren, hält der Linux
Page Cache **eine** Kopie der Bytes für alle gleichzeitig genutzten
Container.

## Function-Manifest (`cfunc.yaml`)

Optional, für lokale Entwicklung & Deployment-Werkzeuge:

```yaml
name: my-fn
runtime: python-3
binary: ./handler.py
layers:
  - shared-config@1.0
  - pylib@2.0
```

`internal/manifest.Load(path)` parst und resolved den Binary-Pfad relativ
zum Manifest.

## Deployment

**Static (Startup):**

```sh
cfunc-gateway -fn=hello -binary=/tmp/example
```

Eine Function, registriert beim Boot.

**Dynamic (Runtime):**

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

Re-Register desselben Namens ersetzt die Definition und schließt den
laufenden Pool graceful.

### Multi-Tenant-Deployments (ab 0.3)

Im Cluster-Mode gehört jede Function zu einem **Projekt**. Beim
Registrieren wird das Projekt mitgegeben; ohne Angabe ist es `default`:

```json
{
  "name": "my-fn",
  "binary": "/abs/path/handler",
  "project": "acme"
}
```

Aufruf dann unter `/v1/acme/fn/my-fn`. Die Legacy-URL `/fn/my-fn`
funktioniert weiterhin, aber nur für `default` — Cross-Projekt-Aufrufe
werden auf Routing-Ebene mit 404 abgewiesen.

API-Key-Bearer brauchen Scope `deploy` zum Registrieren und `invoke`
zum Aufrufen. Vollständige Key-/Quota-/Audit-Story:
[`betrieb.md`](./betrieb.md).

## Concurrency und Recursion

Pro Function gibt es einen **Instance-Pool**. `max_concurrency` legt die
maximale Größe fest (Default 4). Reicht der Pool nicht, blockieren weitere
Anfragen am ältesten Slot.

**Recursive Functions** (Function ruft sich selbst via `/fn/<self>`):

- Synchroner Wait würde den Pool deadlocken (Parent hält Slot, Child
  wartet auf Slot).
- Lösung: **Fire-and-Forget** — Parent dispatched Children und kehrt
  sofort zurück. Beispiel im Scraper-Template:
  `templates/python/scraper/scrape.py`.
- Pool großzügig dimensionieren (`max_concurrency: 12+`) wenn Fan-out hoch.

## Scheduler (Cron)

Function periodisch triggern lassen:

```sh
cfunc cron add --id daily --schedule "0 9 * * *" --function reports
```

Schedule-Format: 5-Feld Standard-Cron (`min hour dom mon dow`) oder
`@every 30s` / `@hourly` / `@daily`.

Der Scheduler-Daemon ruft `/fn/<name>` zur fälligen Zeit an, also greift
der normale Spawn-on-Demand-Pfad — keine spezielle Function-Variante nötig.

## Lokales Testen

**Direktes Skript-Aufrufen** ohne Gateway funktioniert nicht (CFUNC_SOCKET
fehlt). Stattdessen:

1. Gateway lokal starten
2. Function via Admin-API registrieren
3. `curl` an `/fn/<name>`

Für Unit-Tests des Handler-Codes: in Go den Handler direkt aufrufen,
`cfunc.Event`/`cfunc.Response` synthetisieren. Analog Python/Node.

Die SDKs selbst haben Test-Suites:

```sh
go test ./sdks/go/...
python3 -m unittest sdks/python/test_cfunc
node --test sdks/node/cfunc/test_cfunc.mjs
```

## Wire-Protokoll (für SDK-Implementierer)

Frame: `[4 bytes BE length N][N bytes JSON]`. `N <= 16 MiB`.

Frame-Typen:

| Type           | Richtung | Zweck                                 |
|----------------|----------|---------------------------------------|
| `init`         | → fn     | optionales Setup, Reply `init_ok`     |
| `invoke`       | → fn     | Handler-Aufruf, Reply `result`/`error`|
| `result`       | ← fn     | Erfolg                                |
| `error`        | ← fn     | Exception, Stack-Trace inkludiert     |
| `shutdown`     | → fn     | Graceful Stop, Reply `shutdown_ok`    |

Sequentiell pro Connection. Ein Invoke pro Zeit pro Instance.

Implementierungs-Referenz: `internal/wire` (Go), `sdks/python/cfunc.py`,
`sdks/node/cfunc/index.mjs`.

## Best Practices

- **Idempotenz**: Functions sollten mehrfache Aufrufe verkraften
  (Scheduler retried bei Fehlern nicht automatisch, aber Fire-and-Forget
  Recursion kann Doubles erzeugen).
- **Statefulness**: Nichts annehmen, was über einen Cold-Start hinaus
  überlebt. Caches im Prozess sind OK (z.B. ML-Modell), aber nach
  Idle-Reap weg.
- **Side-Effekte**: DB-Verbindungen pro Invoke öffnen oder über
  Connection-Pool — keine globalen Locks ohne Cleanup.
- **Fehlerbehandlung**: Panics werden vom SDK abgefangen → Error-Frame mit
  Stack-Trace. HTTP-Status: 500. Für 4xx-Fehler explizit `Response{Status: 4xx}`
  zurückgeben, nicht raisen.
- **Logs**: stdout/stderr werden vom Gateway durchgereicht und tauchen in
  den Gateway-Logs auf.
