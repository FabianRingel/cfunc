# Betriebsdokumentation

> Wie man einen cfunc-Stack startet, konfiguriert, absichert und überwacht.
> Für **Function-Author** siehe [`entwicklung.md`](./entwicklung.md), für
> einen **KI-Agent** siehe [`agent.md`](./agent.md).

## Überblick

cfunc ist ein selbstgehosteter FaaS-Runner. Eine Anfrage durchläuft:

```
Client ─► Public-Port :8080  /fn/<name>
              │
              ▼
         Gateway
              │  spawnt Function-Instance on demand
              ▼
        Function-Prozess (Subprocess oder OCI-Container)
              │  Wire-Protokoll über Unix-Socket
              ▼
        User-Handler in Go / Python / Node
```

**Zwei Listener:**

| Port  | Default            | Inhalt                                           |
|-------|--------------------|--------------------------------------------------|
| Public  | `:8080`           | `/fn/<name>` (Function-Aufrufe), `/healthz`      |
| Admin   | `127.0.0.1:8081`  | `/_/` Dashboard, `/_/api/*` Admin-API, `/_/ws`   |

Der Admin-Port bindet **per Default nur auf Loopback**. Wer ihn öffentlich
machen will, muss ein Token konfigurieren — andernfalls verweigert das
Gateway den Start.

## Komponenten

| Binary           | Zweck                                                                |
|------------------|----------------------------------------------------------------------|
| `cmd/gateway`    | HTTP-Frontend, Spawner, Pool-Management, Dashboard                   |
| `cmd/scheduler`  | Cron-Daemon, ruft Functions zeitgesteuert via Gateway-HTTP auf       |
| `cmd/cfunc`      | CLI für Layer-Verwaltung und Cron-Jobs                               |

## Voraussetzungen

- **Go ≥ 1.26** (Build)
- **Node ≥ 18** (Dashboard-Build, Node-Functions)
- **Python ≥ 3.11** (Python-Functions)
- **Docker** oder **runc** (Container-Modus, optional)
- **Lima** (macOS-Dev-VM für runc-Tests, optional)

## Build

```sh
make dashboard     # baut React-Bundle (einmalig oder nach UI-Änderungen)
make build         # baut alle Go-Binaries
make test          # alle Go-Tests
```

`internal/dashboard/web/dist/` wird vom Go-Embed-Mechanismus eingebunden;
nach jeder UI-Änderung muss `make dashboard` laufen, sonst sieht das Binary
veraltete Assets.

## Start (lokal)

```sh
go run ./cmd/gateway -addr=:8080 -admin-addr=127.0.0.1:8081
```

Das Dashboard ist dann unter `http://localhost:8081/_/` erreichbar,
Function-Aufrufe gehen an `http://localhost:8080/fn/<name>`.

## Konfigurations-Flags

```
-addr            Public listen address (default :8080)
-admin-addr      Admin listen address (default 127.0.0.1:8081)
-dash            Dashboard URL prefix (default /_/, "" deaktiviert)
-admin-token     Admin token literal (env/file bevorzugen)
-admin-token-file  Datei mit Token
-fn              Optionaler initialer Function-Name
-binary          Optionales initiales Binary (mit -fn)
```

Token-Quellen, in dieser Reihenfolge:

1. `-admin-token-file <pfad>`
2. Env-Var `CFUNC_ADMIN_TOKEN`
3. `-admin-token <wert>` (taucht in `ps`-Listing auf — nur für Lokales)

## Sicherheit

| Konstellation                            | Verhalten                              |
|-------------------------------------------|----------------------------------------|
| `-admin-addr=127.0.0.1:*`, kein Token     | Läuft offen — Loopback isoliert        |
| `-admin-addr=0.0.0.0:*` ohne Token        | Refuse-to-start mit klarer Fehlermsg.  |
| `-admin-addr=*` mit Token                 | Bearer- oder `?token=`-Auth Pflicht    |

Token-Übergabe:

```sh
# Header (curl, fetch)
curl -H 'Authorization: Bearer SECRET' http://host:8081/_/api/state

# Query (Browser-WebSocket — kann keine Header setzen)
http://host:8081/_/api/state?token=SECRET
```

Compare ist constant-time. Token-Rotation: Datei tauschen + SIGTERM+Restart.

## Erste Function deployen

```sh
# 1. Beispiel-Binary bauen
go build -o /tmp/example ./templates/go/example

# 2. Per Admin-API registrieren
curl -X POST http://127.0.0.1:8081/_/api/functions \
  -H 'Content-Type: application/json' \
  -d '{"name":"hello","binary":"/tmp/example"}'

# 3. Aufrufen
curl http://127.0.0.1:8080/fn/hello
# {"hello":"world","method":"GET","path":"/fn/hello"}
```

Beim ersten Aufruf wird die Instance gespawnt (Cold-Start), folgende
Aufrufe trifft sie warm. Nach `IdleTTL` (default 30 s) ohne Aufruf wird
sie wieder beendet.

## Admin-API

| Methode | Pfad                          | Wirkung                          |
|---------|-------------------------------|----------------------------------|
| GET     | `/_/api/state`                | Snapshot (Functions + Layers)    |
| POST    | `/_/api/functions`            | Function registrieren/ersetzen   |
| DELETE  | `/_/api/functions/<name>`    | Function entfernen, Pool killen  |
| GET     | `/_/ws`                       | WebSocket: state + log stream    |

Body von `POST /_/api/functions`:

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

Re-Register desselben Namens schließt die laufenden Pool-Instances graceful
und nimmt die neue Definition an.

## Dashboard

`http://<admin-host>:<admin-port>/_/`

- **KPIs**: req/s, err/s, warm fns, error rate (Live-Push via WebSocket)
- **Charts** (rolling 2 min): Requests/sec, Errors/sec, Warm count, Avg latency, Top-Functions
- **Layers**: aggregiert nach Host-Path → zeigt Page-Cache-Sharing
- **Functions**: Tabelle mit Pool-Status, klickbar für Details
- **Live-Logs**: SSE-WS-Stream mit Filter, Level-Selector, Clear

Token-Login bei aktivierter Auth: Eingabe einmalig pro Tab, gespeichert in
`sessionStorage`.

## Logging

- Gateway loggt strukturiert via `log/slog` nach stderr.
- Wichtige Events: `spawned` (mit `cold_start_ms`, `mode`, `pool_size`),
  `invoke` (mit `request_id`, `duration_ms`, `mode`), `invoke failed`,
  `cron fire`.
- Dashboard liest aus einem Ring-Buffer (1000 Events) mit Live-Push.

## Cron / Scheduler

Eigener Daemon `cmd/scheduler` oder als Library in `internal/scheduler`.

```sh
# Job hinzufügen
cfunc cron add --id daily --schedule "0 9 * * *" --function reports

# Jobs auflisten / entfernen
cfunc cron list
cfunc cron rm daily

# Daemon starten (lädt $CFUNC_STORE/cron.json)
cfunc-scheduler -store=/var/lib/cfunc/cron.json -gateway=http://127.0.0.1:8080
```

`SIGHUP` an den Daemon → Reload der Job-Liste ohne Restart.

Storage: einfache JSON-Datei. Persistent über Restarts. Für Multi-Host
später swappable (SQLite ist offen).

## Layer-Builder (`cmd/builder`)

Layers werden **server-seitig** gebaut, nicht auf der Operator-Workstation.
Das schließt den klassischen Supply-Chain-Vektor (kompromittierte
Dev-Maschine produziert kompromittierten Layer) und erzwingt
Hash-Pinning.

**Builder starten** (auf einem dedizierten Build-Host):

```sh
echo "long-random-secret" > /etc/cfunc/builder.token
chmod 600 /etc/cfunc/builder.token
cfunc-builder \
  -addr=127.0.0.1:9090 \
  -token-file=/etc/cfunc/builder.token \
  -allow-python="3.11,3.12" \
  -allow-index="https://pypi.org/simple"
```

**Gateway ans Builder anschließen:**

```sh
cfunc-gateway ... \
  -builder-url=http://127.0.0.1:9090 \
  -builder-token-file=/etc/cfunc/builder.token
```

**Layer bauen über die CLI:**

Die `requirements.txt` muss **vollständig hash-gepinnt** sein. Sonst
wird der Build mit 400 abgelehnt, bevor `pip` überhaupt läuft.

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

Hash-Pinning erzeugen mit `pip-compile --generate-hashes` aus
`pip-tools` oder mit `pip install pip-compile && pip-compile`.

## Layer-Store

Layers sind **content-addressed Verzeichnisse** auf dem Host, die in
mehrere Function-Container read-only gemountet werden. Identische Pfade =
identischer Inode = ein Set Pages im Linux Page Cache, geteilt über alle
Container.

```sh
# Layer aus existierendem Verzeichnis registrieren
cfunc layer add --name shared --version 1.0 \
  --mount /opt/layers/shared --from /pfad/zu/inhalt

# Python-Layer aus pip-Requirements
cfunc layer build-python --name pylib --version 1.0 \
  --requirements requirements.txt
```

Default-Store: `$CFUNC_STORE/layers/` (oder `/var/lib/cfunc/layers/`).

Function referenziert Layer beim Register:

```json
"layers": [{"name":"pylib@1.0","host_path":"...","mount_path":"/opt/layers/pylib"}]
```

Im Dashboard zeigt das Layers-Panel die Zahl der Referenzen pro Layer und
die effektive Dedup-Quote.

## Container-Modus (Linux + runc)

Auf Linux mit installiertem `runc`:

- `internal/spawn.StartRunc` baut OCI-Bundle mit Read-Only-Rootfs,
  Bind-Mounts der Layers + des Sockets, Standard-Linux-Namespaces
- Standard-Capability-Set wie Docker
- macOS-Dev: VM via `make lima-up` (Lima + Ubuntu + runc + Go).

Die Spawn-Wahl (Subprocess vs. runc) ist im `cmd/gateway`-Setup
hartcodiert. Für Production-Linux: `gateway.Options.Spawn` auf
`StartRunc`-Closure setzen.

## TLS / ACME

cfunc kann selbst Let's-Encrypt-Zertifikate aushandeln und erneuern (via
`certmagic` + libdns). Kein externer Reverse-Proxy nötig.

**Public-Port mit HTTP-01 (einzelne Domain, kein Wildcard):**

```sh
cfunc-gateway \
  -addr=:443 \
  -tls-domain=fn.example.org \
  -tls-email=ops@example.org
# Port 80 muss erreichbar sein für die ACME-Challenge.
```

**Public-Port mit DNS-01 (Wildcards möglich, kein Port 80 nötig):**

```sh
HETZNER_DNS_API_TOKEN=… cfunc-gateway \
  -addr=:443 \
  -tls-domain="fn.example.org,*.fn.example.org" \
  -tls-email=ops@example.org \
  -tls-dns-provider=hetzner
```

**Mitgelieferte Provider:**

| Provider | Env-Vars |
|---|---|
| `cloudflare` | `CF_API_TOKEN` (Zone:Read + DNS:Edit) |
| `hetzner` | `HETZNER_DNS_API_TOKEN` (DNS-Console-Token, NICHT Cloud-API) |
| `route53` | AWS-Standard-Chain (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`) |
| `digitalocean` | `DO_AUTH_TOKEN` |
| `rfc2136` | `RFC2136_SERVER`, `RFC2136_KEY_NAME`, `RFC2136_KEY_ALG`, `RFC2136_KEY` |

**Admin-Port mit eigenem Cert:**

```sh
cfunc-gateway \
  -admin-addr=:8443 \
  -admin-tls-domain=admin.example.org \
  -tls-email=ops@example.org \
  -tls-dns-provider=hetzner \
  -admin-token-file=/etc/cfunc/admin.token
```

**Weitere Flags:**

| Flag | Zweck |
|---|---|
| `-tls-storage <pfad>` | Cert-Cache-Verzeichnis (default: certmagic-Standard) |
| `-tls-staging` | Let's Encrypt Staging (zum Testen, vermeidet Rate-Limits) |
| `-tls-http-addr` | Port für HTTP-01 + HTTP→HTTPS-Redirect (default `:80`) |

**Erstes Setup testen:**

```sh
# Mit Staging-CA, vermeidet LE-Rate-Limits:
cfunc-gateway -tls-domain fn.test ... -tls-staging
# Wenn das durchgeht, das Flag entfernen für echte Certs.
```

Renewal läuft automatisch im Hintergrund; kein Restart nötig. Cert-Files
werden im `tls-storage`-Pfad gehalten und über `flock` gegen
gleichzeitigen Zugriff geschützt — mehrere Gateway-Instances können sich
denselben Storage teilen.

## Health-Check

```sh
curl http://localhost:8080/healthz   # 200 ok
```

Auf dem Admin-Port gibt es bewusst keinen Health-Endpoint ohne Token —
für Loadbalancer-Probes den Public-Port nutzen.

## Troubleshooting

| Symptom                                    | Diagnose / Fix                                    |
|--------------------------------------------|---------------------------------------------------|
| `spawn: timeout waiting for user process`  | Binary findet seine Deps nicht; PATH/PYTHONPATH/`node_modules`-Setup prüfen |
| `function not found: X`                    | Nicht registriert oder seit Restart vergessen     |
| Dashboard zeigt nichts, WS-Disconnect      | Auth-Token fehlt oder falsch; Browser-Console     |
| `pool=N/M` mit M < N                       | Race in einer alten Version; auf aktuell upgraden |
| Function-Counter resettet auf 0            | Pool wurde reaped; Lifetime-Counter ab Pool-Refactor (#49) |
| `runc run failed: ... permission denied`   | Rootfs-/Mount-Source nicht traversierbar; Mode 0755 |
| `runc run failed: rootless ... user namespaces` | Spec mit User-NS bauen oder als root laufen lassen |

Für tiefere Analyse: Dashboard-Logs filtern, oder `slog` direkt mitlesen
(`stderr` des Gateway-Prozesses).

## Stack komplett stoppen

```sh
pkill -TERM cfunc-gateway
pkill -TERM cfunc-scheduler
docker rm -f cfunc-pg     # falls Postgres-Demo
```

Functions sterben mit dem Gateway (Pool wird im Close-Pfad heruntergefahren).
