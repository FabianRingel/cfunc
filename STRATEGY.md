# cfunc — Projekt-Strategie

> Lebendiges Strategie-Dokument. Hier landen Vision, Designprinzipien,
> Architekturentscheidungen und offene Fragen — alles, was länger
> relevant bleibt als ein einzelner Sprint. Die operative Phasen-Liste
> mit Tasks lebt in [`PLAN.md`](./PLAN.md).
>
> **Stand:** 2026-04-26 · **Status:** Single-Node 0.1 funktional, 0.2 in
> Planung.

---

## 1. Mission

**Self-hosted Function-as-a-Service für die europäische Cloud —
single-binary, EU-Jurisdiktion by default, Lambda-kompatibel genug.**

Wir bauen die Alternative, die fehlt: ein Open-Source-FaaS-Stack, den
ein Team mit `terraform apply` auf 3 Hetzner-Servern ausrollt, das
DSGVO-konform betrieben werden kann, ohne Daten in US-Clouds zu
kippen, und das genug Lambda-Funktionsumfang hat, um typische
Cloud-Function-Workloads abzulösen — nicht das Vollprogramm von AWS,
aber die 80 %, die in der Praxis genutzt werden.

## 2. Designziele

| Ziel | Konsequenz |
|---|---|
| **GDPR-konform, EU-only deployable** | Keine Telemetrie, alle State-Komponenten unter Operator-Kontrolle, Audit-Log mandatory, Region-Tagging im Datenmodell |
| **Hetzner-Cluster (3–10 Nodes typisch)** | Kein k8s-Zwang, aber k8s-kompatibel; Bare-Metal/VM via Terraform+Ansible muss funktionieren; Single-Node-Setup bleibt einfach |
| **Lambda-Workloads abdecken** | HTTP-Trigger, Cron, später API-Routes, Queue-Trigger, Event-Trigger |
| **Multi-Tenant** | Projekte als first-class, RBAC, Quotas, isolierte Token-Hierarchien |
| **OSS-Adoption** | Trivialer Quickstart („helm install"/„terraform apply"), gute Doku, ehrliche Benchmarks, Migration-Guide von Lambda |
| **Skalierung als Default** | Stateless Control-Plane von Tag 1, kein „später migrieren wir auf Postgres"-Tech-Debt |

## 3. Architekturpfeiler

### 3.1 Stateless Control-Plane mit Postgres als einziger Truster

Postgres ist die einzige Persistenz-Instanz im Cluster. Keine etcd-Soup,
keine zwei verschiedenen Storage-Engines.

**Warum Postgres:**
- `LISTEN/NOTIFY` für Live-Invalidation zwischen Replicas
- `pg_try_advisory_lock` für Cron-Leader-Election ohne externe Lib
- Transaktionen, Backup-Stories, EU-Hosting bei jedem Anbieter verfügbar
- Operatives Wissen flächendeckend vorhanden

**Tabellen-Skizze:**

```
projects             id, name, region, quota_*, ...
api_keys             project_id, key_hash, scopes, ...
functions            project_id, name, binary_layer_digest, env, ...
cron_jobs            project_id, function_id, schedule, ...
layer_refs           project_id, layer_name, layer_version, registry_url, digest
audit_log            ts, project_id, principal, action, target, before, after
gateway_nodes        id, region, last_seen
function_assignments function_id, gateway_id   ← optional, für Sticky-Routing
```

### 3.2 Layer-Distribution via OCI-Registry

Layers sind OCI-Images in einer Registry (zot, Harbor) — nicht NFS.
Builder pusht beim Build, Gateway pullt beim Erstkontakt mit lokalem
LRU-Cache.

**Warum OCI:**
- Standard-Tooling: cosign-Signaturen, Mirroring built-in, S3-Backend, alles erprobt
- Kein Shared-FS-Single-Point-of-Failure
- Funktioniert über Region-Grenzen via Registry-Replication
- Kein cfunc-spezifisches Format

**Page-Cache-Sharing pro Knoten** bleibt erhalten. **Cluster-weit** geht
das physikalisch nicht (jeder Kernel hat seinen eigenen Page-Cache) →
Mitigation = Sticky-Routing.

### 3.3 Sticky-Routing für Warm-Affinität

Function-Name → consistent-hash → bevorzugter Node. Anfragen an
`tenant1/numpy-search` landen primär auf demselben Knoten — dort ist der
Layer warm und der Pool aufgewärmt.

**Implementierungs-Pfade:**
- v1: HAProxy mit `hash-type consistent` davor — null Code, sofort einsetzbar
- v2: Eigener Router der Postgres-Assignments respektiert + Capacity-aware Failover

Knative löst das mit „Activator + Autoscaler" — wir wiederholen den
Pattern, aber simpler weil wir kein k8s zugrunde haben.

### 3.4 Multi-Tenancy als first-class

- **Projekte** als Container für Functions/Crons/Layers
- **API-Keys** mit Scopes (`functions:write`, `layers:read`, `cron:write`, `dashboard:read`)
- **Quotas pro Projekt:** max Functions, max Concurrency-Sum, max Storage, RPS-Caps
- **Routing-Pfad:** `/v1/<project>/fn/<name>` (Sub-Domain als optionales Setup)
- **Audit-Log:** Pflicht für DSGVO

Spawn-Isolation bleibt wie heute: runc + Layer-Mount + sanitized env.
**Multi-Tenant heißt Logical-Separation auf Control-Plane-Ebene, harte
Isolation auf Container-Ebene.**

## 4. Was bleibt per-Node, was wird shared

| Was | Bleibt per-Node | Warum |
|---|---|---|
| Warm-Instance-Pool | ✅ | Subprocess gehört zu seinem Host-Kernel; Lambda + Cloud Run machen es genauso |
| Linux-Page-Cache zwischen Functions | ✅ | Kernel-Eigenschaft, kein Verteilungsmechanismus; Mitigation = Sticky-Routing |
| Idle-Reaper, Cold-Start-Metriken | ✅ | An Pool-Lifecycle gebunden |
| Layer-Cache | ✅ | Lokaler Disk-Cache pro Replica, gefüllt per OCI-Pull |

| Was | Wird shared | Warum |
|---|---|---|
| Function-Definitionen | Postgres | Sonst hängt Definition davon ab, an welche Replica registriert wurde |
| Cron-Jobs | Postgres + Leader-Election | Genau **einer** darf feuern |
| Layer-Inhalte | OCI-Registry | Jeder Knoten muss Layer holen können |
| Admin-/API-Tokens | Postgres | Konfiguration auf allen Replicas identisch |
| Audit-Log | Postgres | DSGVO-Pflicht, eine Source of Truth |
| Quotas / Rate-Limits | Postgres + Token-Bucket pro Replica mit periodischer Reconciliation | Lokale Zähler, Cluster-weite Caps |

## 5. Release-Roadmap

| Release | Inhalt | Aufwand |
|---|---|---|
| **0.1** | Single-Node, 1 Tenant, voll funktional. Wire/Pool/Layers/Scheduler/Dashboard/TLS/Builder steht | ✅ |
| **0.2 — Cluster-Ready** | `internal/state` mit Postgres-Backend + LISTEN/NOTIFY, Cron-Leader-Election via `pg_try_advisory_lock`, `cfunc cluster init/status`, Gateway `-state-dsn`. `internal/layerstore` mit S3-Backend (Hetzner Object Storage / RustFS / AWS) als isolierte, getestete Schicht. | ✅ |
| **0.2.1** | Multi-stage Dockerfile, docker-compose-Stack (2 Gateways + Postgres + RustFS + Builder), Helm-Chart in `deploy/helm/cfunc/`, Hetzner-Quickstart-Doku | ✅ |
| **0.3 — Multi-Tenancy** | Projekte als Tenant-Einheit, API-Keys mit Scopes (`admin`/`deploy`/`invoke`), Per-Projekt-Quotas mit Token-Bucket + Postgres-Aggregat-Sync, Append-Only-Audit-Log, URL-Path-Routing (`/v1/<project>/fn/<name>`) mit Compat-Pfad. Digest in `LayerMount` + `StoreResolver` der Layer aus S3-Backend pullt und tar-slip-gehärtet entpackt. CLI: `cfunc project|key|quota|audit`. | ✅ |
| **0.4 — Sticky-Routing & Performance** | Eigener Router oder HAProxy-Recipe, Cold-Start-Optimierungen, Pre-Warming-API (`min_warm: N`), Builder-seitiger Push in Layerstore (push-on-build statt nur pull-on-reference) | ~2 Wochen |
| **0.5 — Lambda-Parity-Trigger** | API-Gateway-Routes (Path/Method/Headers), Queue-Trigger via NATS oder Postgres, S3-Event-Trigger (RustFS/MinIO-kompatibel) | ~3 Wochen |
| **0.6 — Operator-Suite** | Terraform-Modul für Hetzner, Ansible-Playbook, Prometheus-Exporter, Grafana-Dashboards, Backup-Tooling | ~2 Wochen |
| **0.7 — Sicherheits-Hardening** | Layer-Signaturen-Pflicht (cosign), Policy-Engine (OPA-light), Network-Policies, User-Namespace-Isolation pro Function | ~2 Wochen |
| **1.0 — Production-Ready** | Doku, Benchmarks, Lambda-Migration-Guide, Code-of-Conduct, CONTRIBUTING.md, Issue-Templates, Release-Pipeline | ~2 Wochen |

**Gesamtweg: ~4–5 Monate konzentrierter Arbeit für 1.0**, jedes Release
für sich einsetzbar.

## 6. Konkrete nächste Schritte (Release 0.4)

Mit 0.3 ausgeliefert sind: Multi-Tenancy-Datenmodell, Auth, Routing,
Quotas, Audit, content-addressed Layer-Pull. Was 0.4 anfasst:

### Stück A: Sticky-Routing
- Function-Name → Backend-Hash an Load-Balancer (HAProxy-Recipe oder eigener TCP-Router)
- Reduziert Cold-Starts, weil wiederholte Calls auf denselben Gateway treffen und dort den warmen Pool finden
- Warm-Pool-Effekt war bisher ungenutzt im Cluster, weil LB round-robin verteilt

### Stück B: Pre-Warming-API
- `min_warm: N` als Function-Konfig
- Gateway hält N Instanzen permanent warm
- MaxConcurrency-Pool teilt sich in `min_warm` (resident) + `max_burst` (transient)

### Stück C: Builder-Push in Layerstore
- Heute: 0.3 hat den Pull-Pfad gewired (Gateway → S3 bei erster Reference)
- 0.4 schließt den Kreis: Builder pusht eine fertig-gebaute Schicht direkt nach Build in S3
- Manifest aktualisiert den Digest im LayerMount
- cosign-Signaturen optional (volle Pflicht in 0.7)

## 7. Offene Designentscheidungen

Hier sammeln wir Fragen, die wir **bewusst noch nicht** beantwortet haben.
Wenn die Antwort gefunden ist, wandert der Eintrag in die Architektur-
Sektion.

### 7.1 Multi-Tenancy-Routing-Modell — **entschieden in 0.3: URL-Path**
- Implementiert: `/v1/<project>/fn/<name>` — `parseFunctionPath` erkennt beide Formen.
- `/fn/<name>` bleibt als Compat-Pfad für Single-Tenant-Deployments (Project = `default`).
- Sub-Domain-Routing bleibt für später optional; bisher kein Operator-Bedarf gemeldet.

### 7.2 Container-Isolation auf Multi-Tenant-Level
- Heute: alle Functions im selben runc-Bundle-Schema, Standard-Capability-Set
- Multi-Tenant-Production sollte: User-Namespace pro Function (rootless im Container), eigenes Network-Namespace mit Veth, Memory/CPU-cgroups
- **Offen:** Wann härten wir? Vor 1.0 oder als 1.x-Feature?

### 7.3 Cold-Start-Strategie für Multi-Tenant
- Lambda hat „provisioned concurrency" — Pre-Warm auf Operator-Anforderung
- **Vorschlag:** pro Function `min_warm: N` als Konfig-Option; Gateway hält N Instanzen permanent warm, MaxConcurrency-Pool wird in `min_warm + max_burst` aufgeteilt
- **Offen:** Wie wird das in der Quota verbucht — pre-warmed-Slot kostet RAM permanent

### 7.4 Function-Code als Layer
- Heute: Function-Binary ist eine separate Datei, Layer sind Daten daneben
- Konsequente Vereinheitlichung: Function-Code IST ein Layer (`runtime: "go-binary"`), wird via `cfunc deploy` als OCI-Image gepusht — alles ist content-addressed, alles geht durch dieselbe Pipeline mit denselben Sicherheitschecks
- **Tendenz:** Vereinheitlichen, aber nicht vor 0.3 — erst Multi-Tenancy-Datenmodell stabilisieren

### 7.5 Queue-Trigger: welcher Broker?
- **NATS** — leichtgewichtig, single-binary, EU-friendly
- **Postgres als Queue** (LISTEN/NOTIFY oder `SKIP LOCKED`-Pattern) — null Extra-Komponente
- **Redis** — verbreitet, aber Memory-only Default und schwergewichtige Cluster-Story
- **Tendenz:** Postgres-Queue für 0.5 (kein neuer Stack-Member), NATS als optionaler Backend für hochfrequente Workloads

### 7.6 Event-Trigger: S3-kompatibel?
- RustFS (Apache-2.0, Rust, MinIO-Nachfolger) und Hetzner Object Storage emittieren S3-style Events
- **Tendenz:** Wir konsumieren S3-Events via NATS oder Webhook; bauen kein eigenes Object-Storage. Lokale Tests laufen gegen RustFS (MinIO-Repo wurde archiviert, Projekt eingestellt)

### 7.7 OSS-Lizenz — **entschieden: Apache 2.0**
**Apache License, Version 2.0** (siehe [`LICENSE`](./LICENSE), [`NOTICE`](./NOTICE)).
SPDX-Header (`Apache-2.0`) sind in allen Source-Files. Begründung: maximal
permissiv für Distributoren, die auf cfunc aufsetzen und kommerzielle
Compute-Ressourcen weiterverkaufen wollen — passt zur Mission „self-hosted
FaaS für die EU-Cloud, jeder darf seine eigene Distribution bauen".
Patent-Grant ist explizit (Abschnitt 3 der Lizenz), Trademark-Schutz
bleibt (Abschnitt 6) — der Name „cfunc" ist nicht automatisch frei, der
Code schon.

### 7.8 Repo-Strategie
- **Monorepo** (`github.com/fabianringel/cfunc`): einfach, eine Versionsnummer, atomische Cross-Komponenten-Refactors
- **Multi-Repo** (`github.com/cfunc/{gateway,builder,dashboard,terraform-modules,helm-chart}`): klarere Boundaries, separate Release-Cycles, Community-Friendlier
- **Tendenz:** Monorepo bis ~1.0, danach Helm-Chart und Terraform-Modul ausgliedern

### 7.9 Wann gibt es eine eigene Org?
- Heute: persönlicher Fork
- Bei OSS-Launch: `github.com/cfunc-io` oder ähnlich
- **Trigger:** vor 0.5-Release / vor erstem öffentlichen Showcase

## 8. Marketing & Positionierung

### Positionierungs-Satz
> **„Self-hosted FaaS for the European cloud — single-binary,
> EU-jurisdiction by default, Lambda-compatible enough."**

### Konkurrenzanalyse
| Projekt | Stärken | Lücke, die wir schließen |
|---|---|---|
| **OpenFaaS** | k8s-only, große Community, Feature-Komplettpaket | Schwerer Setup, k8s-Pflicht |
| **Knative** | Google-flavor, sehr feature-reich | k8s-Pflicht, hohe Komplexität |
| **Fission** | k8s-only | k8s-Pflicht |
| **fly.io Machines** | Großartiges UX | proprietär, US-Hosting |
| **AWS Lambda / Cloud Run** | Marktführer | US-Cloud, vendor-lock |

**Lücke, die cfunc füllt:** VM-/Bare-Metal-deployable, Single-Binary-Setup,
EU-fokussiert, mit nativer Multi-Tenancy. Sweet-Spot für mittelgroße
Unternehmen / Behörden / EU-Cloud-Reseller.

### Erste-Schritte-Story (das Ziel der README)
```sh
# 3 Hetzner-Server, ein Postgres, ein zot-Container
terraform apply -var "node_count=3"
cfunc cluster init "postgres://…"

# erstes Projekt
cfunc projects create my-team
export CFUNC_TOKEN=$(cfunc projects token my-team)

# Function deployen (5 Sekunden)
cfunc deploy --name=hello --binary=./hello
curl https://my-team.cfunc.example.org/fn/hello
```

### Was wir bewusst NICHT bauen
- **Proprietäre Runtimes** außer Go/Python/Node (Rust/.NET nur wenn Community treibt)
- **Serverless Containers** — das ist k8s, anderer Markt
- **Step-Functions / State-Machines** — separate Werkzeuge sind besser darin
- **Image-basierte Deployments** — Layers sind unsere Image-Abstraktion
- **Telemetrie-Phoning-Home** — Mission ist EU-Souveränität

## 9. Performance-Erwartungen

Diese Zahlen sind unsere Baseline (Single-Node, M-Hardware, gemessen 2026-04):

| Metrik | Baseline | Ziel 1.0 |
|---|---|---|
| Sustained RPS (single node, ~150 ms avg latency) | 18.500 | 25.000 |
| Cold-Start Go-Binary | ~10 ms | <10 ms |
| Cold-Start Python (Modell-Cache warm) | ~300 ms | <200 ms |
| Cold-Start Node-ESM | ~35 ms | <30 ms |
| Avg-Overhead pro Invoke (HTTP→Pool→Wire→Subprocess→…) | ~3 ms | <1 ms |
| Memory pro idle Pool-Slot | ~10 MB | <5 MB |
| Cluster mit 5 Replicas, hot-path RPS | (nicht gemessen) | 100.000 |

Benchmarks gehören in `docs/benchmarks/` und werden Teil jeder
Release-Pipeline.

## 10. Compliance / DSGVO

Was wir explizit garantieren wollen, dokumentiert in `docs/compliance/`:

- **Datenfluss-Diagramm:** wo welche Daten landen, wer sie sieht
- **Audit-Log-Schema:** welche Felder, welche Retention, welche Export-Wege
- **Region-Tagging:** Tenants und Functions können auf Region-Allow-List konfiguriert werden; Routing respektiert das
- **Auftragsverarbeitungsvertrags-Vorlage** (AVV) für Cloud-Provider-Hosting
- **No-telemetry-Garantie:** cfunc selbst sendet keine Daten ins Ausland; nur Operator-konfigurierte Endpoints werden angesprochen
- **Verschlüsselung at-rest:** Layer-Cache + Postgres mit Disk-Encryption-Empfehlung; Token-Hashes statt Klartext

## 11. Community-Aufbau (ab 0.5)

- **CONTRIBUTING.md** mit klaren Regeln (DCO oder CLA-Signoff?)
- **Issue-Templates:** Bug, Feature, Security
- **Code-of-Conduct** (Contributor Covenant 2.1)
- **Diskussionsforum:** GitHub Discussions oder Matrix-Channel — keine Discord-Falle
- **Release-Cadence:** monatliche Minor-Releases, Patches on-demand
- **Security-Disclosure:** SECURITY.md mit GPG-Key, 90-Tage-Disclosure-Policy
- **Roadmap-Transparenz:** dieses Dokument bleibt öffentlich; Quartals-Roadmap-Updates

## 12. Glossar

| Begriff | Bedeutung in cfunc |
|---|---|
| **Function** | Ausführbare Einheit (Go-Binary, Python-Skript, Node-Modul) mit Wire-Contract |
| **Layer** | Read-only Bind-Mount-Quelle, content-addressed, in OCI-Registry gehalten |
| **Project / Tenant** | Logischer Container für Functions, Crons, Layers, Tokens |
| **Pool** | Per-Function-Set warmer Instances auf einem Knoten |
| **Replica** | Eine Gateway-Instanz im Cluster |
| **Wire** | Length-prefixed JSON IPC zwischen Gateway und Function-Prozess |
| **Spawner** | Mechanismus zum Function-Start (Subprocess für Dev, runc für Production) |
| **Sticky-Routing** | Function-Hash-basierte Lastverteilung für Warm-Affinität |

## 13. Änderungs-Log dieses Dokuments

| Datum | Änderung |
|---|---|
| 2026-04-26 | Initiale Version, geschrieben in Sequenz mit Single-Node 0.1-Abschluss |
| 2026-04-26 | Entscheidung 7.7: **Apache 2.0** als OSS-Lizenz. LICENSE + NOTICE angelegt, SPDX-Header in allen Source-Files. |
| 2026-04-26 | 0.2 Code-Side abgeschlossen: A1 (Store-Interface + InMem), A2 (PostgresStore + LISTEN/NOTIFY), A3 (StateScheduler + Leader-Election via `pg_try_advisory_lock`), C (cluster CLI + Gateway `-state-dsn`), B (`internal/layerstore` mit S3-Backend). Live-verifiziert: PUT auf Replica A → binnen 1 s sichtbar auf Replica B. Layer-Distribution-Verdrahtung auf 0.3 verschoben (braucht Digest-in-LayerMount). |
