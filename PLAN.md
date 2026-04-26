# cfunc — Plan & Roadmap

> Selbstgebauter FaaS-Runner mit Container-Isolation, sprach-agnostischem
> Handler-Contract und gemeinsam genutzten Dependency-Layers.

## Vision

Ein leichtgewichtiger Cloud-Function-Runner, der:

- Functions in mehreren Sprachen (Go zuerst, dann Python, Node/TS, Java)
  mit standardisierten Entry-Points ausführt.
- Container-basierte Isolation nutzt (OCI / `runc`).
- **Layer-Sharing auf Kernel-Ebene**: identische Dependency-Layers liegen
  einmal physisch im Page-Cache, nicht pro Container-Instanz.
- **Scale-to-Zero pro Function**: Function ist standardmäßig nicht im
  Speicher, HTTP-Trigger startet sie on-demand, Idle-Timer killt sie.
- **Cron-Manager** integriert, der Functions zeitgesteuert triggert.

## Architektur (High-Level)

```
                       ┌────────────────────────────┐
HTTP Request ─────────▶│  cfunc-gateway             │
                       │  - Routing /fn/<name>      │
                       │  - Spawn-on-demand         │
                       │  - Idle-Killer             │
                       └────────────┬───────────────┘
                                    │ Unix Socket (length-prefixed JSON)
                                    ▼
                       ┌────────────────────────────┐
                       │  cfunc-runtime-<lang>      │
                       │  (im Container, statisch)  │
                       │  - hält Socket             │
                       │  - spawnt User-Subprocess  │
                       │  - leitet Frames durch     │
                       └────────────┬───────────────┘
                                    │ stdio / Socket
                                    ▼
                       ┌────────────────────────────┐
                       │  User-Function (Subprocess)│
                       │  via Sprach-SDK            │
                       └────────────────────────────┘

           ┌────────────────────────────┐
           │  cfunc-scheduler           │── HTTP ──▶ Gateway
           │  - Cron-Definitionen       │
           │  - robfig/cron/v3          │
           └────────────────────────────┘

Layer-Store:  /var/lib/cfunc/layers/<sha>/   (read-only, bind-mounted)
```

## Designentscheidungen (mit Begründung)

| Entscheidung                   | Wahl                                | Warum                                                                 |
|--------------------------------|-------------------------------------|-----------------------------------------------------------------------|
| Isolation                      | OCI via `runc` (später ggf. gVisor) | Standard, kontrollierbare Mounts, kein Docker-Daemon nötig            |
| User-Code-Embedding            | **Modell C** (Subprocess + Socket)  | Sprach-agnostisch; gleiches Modell für Go/Python/Node/Java            |
| IPC                            | Unix-Socket, length-prefixed JSON   | Trivial in jeder Sprache; debuggbar; ausreichend schnell für FaaS     |
| Layer-Sharing                  | Read-only Bind-Mounts on Host       | Linux Page-Cache teilt automatisch via gleichem inode                 |
| Image-Assembly                 | OCI-Manifest komponieren, kein Build| Kein `docker build` pro Deploy; Layers werden referenziert, nicht kopiert |
| Cold-Start-Modell              | Spawn-on-Demand + Idle-Timeout      | Scale-to-Zero pro Function; einfach, später Pool/Snapshots            |
| Scheduler                      | Eigener (robfig/cron/v3)            | Portabel, container-fähig, kein OS-cron                               |
| Control-Plane-Sprache          | Go                                  | OCI-Libs (`go-containerregistry`), schneller Start, statisches Binary |

## Wire-Protokoll

Frames über Unix-Socket: `[4 bytes big-endian length][JSON payload]`.

**Frame-Typen:**

```jsonc
// Init (einmal nach Process-Start, optional)
{ "type": "init", "id": "init-1", "config": { ... } }
{ "type": "init_ok", "id": "init-1" }

// Invoke (pro HTTP-Request)
{ "type": "invoke", "id": "req-abc",
  "event":   { "method": "POST", "path": "/", "body": "...", "headers": {...} },
  "ctx":     { "deadline_ms": 30000, "trace_id": "..." } }

{ "type": "result", "id": "req-abc", "result": { "status": 200, "body": "..." } }
// oder:
{ "type": "error",  "id": "req-abc", "error": { "type": "...", "message": "...", "stack": "..." } }

// Shutdown (vor Idle-Kill, optional)
{ "type": "shutdown", "id": "sd-1" }
{ "type": "shutdown_ok", "id": "sd-1" }
```

Sequentiell pro Socket — eine Function-Instanz bearbeitet einen Request
zur Zeit. Concurrency macht das Gateway durch zusätzliche Instanzen.

## Repo-Struktur

```
cfunc/
  cmd/
    cfunc/          # CLI (deploy, invoke, layer add, cron add, ...)
    gateway/        # HTTP-Frontend
    runtime-go/     # Go-Runtime-Host (im Container)
    scheduler/      # Cron-Manager
  internal/
    wire/           # Length-prefixed JSON Protocol (shared)
    layers/         # Layer-Store, Manifest, Resolver
    spawn/          # OCI-Runtime-Spec-Builder, runc-Aufruf
    registry/       # Function-Registry
  sdks/
    go/             # User-importierbar
    python/         # später
    node/           # später
  templates/
    go/example/     # Starter-Template
  docs/
  PLAN.md
```

## Phasen-Roadmap

### Phase 1 — Wire & Lokaler Lauf (ohne Container)

Ziel: HTTP-Request landet via Gateway → Socket → User-Handler und kommt
zurück. Kein Container, kein Layer-Sharing, kein Cron — nur das Protokoll.

- [ ] 1.1 `internal/wire`: Frame-Encoder/Decoder, Frame-Typen, Tests
- [ ] 1.2 `sdks/go`: `cfunc.Start(handlerFunc)` — User schreibt nur Handler
- [ ] 1.3 `cmd/runtime-go`: spawnt User-Binary als Subprocess, hält Socket
- [ ] 1.4 `cmd/gateway`: HTTP → Wire-Frame → Socket → zurück
- [ ] 1.5 `templates/go/example`: Beispiel-Handler + E2E-Test

### Phase 2 — Container-Isolation

- [x] 2.1 `internal/oci`: OCI-Runtime-Spec-Builder (read-only rootfs, layer
      bind-mounts, isolation namespaces)
- [x] 2.2 `internal/runc`: dünner Wrapper um `runc run/kill/delete` +
      Bundle-Schreiber. Linux-only Integrationstest hinter Build-Tag
      `runc_integration`.
- [x] 2.3 Idle-Killer im Gateway (TTL + injizierbare Clock + Reaper)
- [x] 2.4 Lima-Setup für macOS-Dev (`scripts/lima-setup.sh`,
      `scripts/cfunc-dev.yaml`)
- [x] 2.5 Spawner-Refactor: `Spawner` als injizierbare Funktion im
      Gateway; `spawn.StartRunc` baut OCI-Bundle, bind-mountet User-Binary
      nach `/cfunc`, startet Container via runc, akzeptiert Socket; E2E
      grün (`./scripts/lima-setup.sh test-runc-e2e`)
- [x] 2.6 Strukturiertes Logging mit `log/slog`: `spawned` (mit
      `cold_start_ms`, `mode`) und `invoke` (mit `duration_ms`,
      `request_id`) — gemessene Cold-Start-Zeit für runc ~30 ms im Lima-VM

### Phase 3 — Layer-Store & Sharing

- [x] 3.1 `internal/layers`: Content-addressed Store, sha256 über
      Datei-Tree, Manifest pro Blob, JSON-Index, Add/Resolve/List
- [x] 3.2 `cmd/cfunc layer add/list/show` mit `CFUNC_STORE` Env-Var
- [x] 3.3 `internal/manifest`: `cfunc.yaml`-Parser (yaml.v3)
- [x] 3.4 Gateway `FunctionDef` mit `LayerMount`-Liste; `Spawner` löst
      Layers in `oci.Layer` auf, `RuncOptions.ExtraLayers` greift
- [x] 3.5 **Sharing-Verifikation grün**: zwei runc-Container sehen
      denselben Inode auf demselben Device beim Lesen einer
      Layer-Datei → Linux Page-Cache hält den Layer physisch einmal im
      RAM für beliebig viele Functions (TestLayerSharing_SameInode)

### Phase 4 — Scheduler

- [x] 4.1 `internal/scheduler`: Job-Modell mit Validierung,
      JSON-Persistenz (SQLite zurückgestellt — Single-Host-Scope reicht
      JSON), `robfig/cron/v3`-Wrapper mit Reload + FireNow,
      injizierbarer `Trigger` für Tests
- [x] 4.2 CLI `cfunc cron add/list/rm/run` über `CFUNC_STORE`
- [x] 4.3 `cmd/scheduler`-Daemon: lädt Store, registriert Jobs, SIGHUP
      = Reload, SIGTERM = Shutdown; `HTTPTrigger` ruft Gateway auf
      Standard-Pfad `/fn/<name>` → Spawn-on-Demand greift wie bei
      externer Anfrage; E2E grün

### Phase 5 — Zweite Sprache (Python)

- [x] 5.1 `sdks/python/cfunc.py`: identisches Wire-Protokoll, dataclass-API
      (`Event`/`Context`/`Response`), Panic-zu-Error-Mapping mit Stack-Trace,
      Unit-Tests via `unittest`
- [x] 5.2 `templates/python/example/handler.py` mit Shebang;
      Gateway-Spawner inheritet Host-Env (PATH für `/usr/bin/env python3`);
      `FunctionDef.Env` für PYTHONPATH; E2E grün (Cold-Start ~300 ms)
- [x] 5.3 `cfunc layer build-python --requirements req.txt`: pip install
      in Tempdir → Content-Hash → Store-Add. Verifiziert per E2E-Test
      (`TestPythonLayerSubprocess`): Layer im Store landet bei einem
      Handler an `PYTHONPATH` und das pip-installierte Modul wird
      erfolgreich importiert.
- [x] 5.4 Sprach-Abstraktion validiert: gleicher Gateway, gleicher
      Spawner, gleiches Wire-Protokoll, beliebiger Handler-Lauf.

> **Offen / nach Phase 6:** Echtes Python-Base-Rootfs für runc-Modus
> (Ubuntu-Slim oder debootstrap). Aktuell läuft Python im Container
> noch nicht out-of-the-box, weil das Standard-Base-Rootfs leer ist —
> reicht für statisch gelinkte Go-Binaries, nicht für Interpreter.

### Phase 5b — Operator-Dashboard

- [x] 5b.1 `internal/dashboard.LogCapture`: slog-Handler-Wrapper mit
      Ring-Buffer + Pub/Sub für SSE-Subscriber
- [x] 5b.2 `gateway.Stats()`-Snapshot mit Per-Function-Metriken (Cold-
      Start, Avg-Duration, Invokes, Errors, Idle)
- [x] 5b.3 Dashboard-Handler mit embedded HTML/CSS/JS via `embed.FS`
      → Single-Binary; live State-Polling alle 2 s, Logs via SSE
- [x] 5b.4 Mounted unter `/_/` im Gateway-Binary; `cmd/gateway` setzt
      den `LogCapture` als Default-Logger und teilt ihn mit dem Dashboard

### Phase 6 — Weitere Sprachen

- [x] 6.1 Node/TypeScript: `sdks/node/cfunc/` als ESM-Paket mit
      `package.json` (importbar als `import 'cfunc'`), gleiches
      Wire-Protokoll wie Go/Python, async-Handler-Signatur,
      Sequential-Frame-Reader. 4 Unit-Tests mit Node Test-Runner grün.
      Template `templates/node/example/` mit `npm install` (file:-Dep
      auf SDK). Cold-Start ~35 ms, warm ~0.5 ms.
- [ ] 6.2 Java (optional)

## Dev-Setup (macOS)

`runc` ist Linux-only. Auf macOS läuft cfunc daher in einer Lima-VM:

```sh
brew install lima
./scripts/lima-setup.sh up         # Linux VM mit runc + Go
./scripts/lima-setup.sh test       # alle Tests in der VM
./scripts/lima-setup.sh test-runc  # nur die runc-Integrationstests
./scripts/lima-setup.sh shell      # interaktive Shell in der VM
```

Plattform-unabhängige Tests (`wire`, `sdks/go`, `oci`, Idle-Reaper)
laufen auch nativ auf macOS (`go test ./...`).

## Arbeitsweise

### TDD

- **Test zuerst**, dann Implementation. Jedes Package in `internal/` und
  `sdks/` startet mit `*_test.go`.
- E2E-Tests pro Phase: Phase 1 endet mit grünem E2E-Test, der einen
  Beispiel-Handler über das Gateway aufruft.
- `go test ./...` muss vor jedem Commit grün sein.

### Dokumentation

- Jedes neue Package bekommt einen Header-Kommentar mit Zweck und
  Lebensdauer-Erwartungen.
- Architektur-Änderungen werden in `PLAN.md` festgehalten, nicht nur
  im Code.
- Wire-Protokoll-Änderungen sind versioniert; Frame-`type`-Werte sind
  stabile API.

### Commits

- Pro abgeschlossener Phase-Aufgabe ein Commit.
- Commit-Message beschreibt das *Warum*, nicht nur das *Was*.

## Offene Fragen / Spätere Entscheidungen

- **Layer-Limit:** OCI hat ~125 Layers/Image. Bei mehr Deps: Multi-Layer
  pro Manifest oder Bind-Mount-Modus.
- **Multi-Tenant-Sicherheit:** Reicht `runc` + User-Namespaces, oder
  brauchen wir gVisor/Kata?
- **Persistenz Function-Registry:** SQLite reicht initial, später
  swappable.
- **Beobachtbarkeit:** Strukturiertes Logging (slog) ab Tag 1, Tracing
  später (OpenTelemetry).
