# Hetzner Quickstart

A reference deployment of cfunc on Hetzner Cloud: 3 gateway nodes,
managed Postgres, Object Storage for layers, ACME via Hetzner DNS.
The whole setup is EU-jurisdiction (Falkenstein or Helsinki) and runs
on roughly **€25–40/month** for a small workload.

This guide assumes you already have a Hetzner Cloud account, the
[`hcloud`](https://github.com/hetznercloud/cli) CLI, and a domain
whose DNS is delegated to Hetzner.

---

## What you'll end up with

```
                   fn.example.org (A → load-balancer)
                              │
                  ┌───────────┴───────────┐
                  │  Hetzner Load Balancer│
                  └─┬─────────┬─────────┬─┘
                    │         │         │
              ┌─────▼─┐ ┌─────▼─┐ ┌─────▼─┐
              │ gw-a  │ │ gw-b  │ │ gw-c  │   cx22 (2 vCPU, 4 GB)
              └───┬───┘ └───┬───┘ └───┬───┘
                  │         │         │
        ┌─────────┴─────────┴─────────┘
        │
    ┌───▼─────────────────┐    ┌───────────────────────┐
    │ Hetzner Postgres    │    │ Hetzner Object Storage│
    │ (cluster state,     │    │ (layer distribution,  │
    │  cron leader)       │    │  S3-compatible)       │
    └─────────────────────┘    └───────────────────────┘
```

Per-host page-cache layer sharing means you do **not** want auto-scaling
gateways — each gateway maintains its own warm pool. Three fixed
nodes is the sweet spot for ~10k req/s with HA.

---

## 1. Provision Postgres

Use Hetzner's managed Postgres (or a `cx22` running Postgres yourself).
Required: Postgres ≥ 14.

```sh
# Create database for cfunc state
psql "$ADMIN_DSN" -c "CREATE DATABASE cfunc;"
psql "$ADMIN_DSN" -c "CREATE USER cfunc WITH PASSWORD '…';"
psql "$ADMIN_DSN" -c "GRANT ALL ON DATABASE cfunc TO cfunc;"
```

Connection string for the gateways:

```
postgres://cfunc:…@db.hetzner.internal:5432/cfunc?sslmode=require
```

## 2. Provision Object Storage (optional, for 0.3+)

```sh
hcloud object-storage bucket create cfunc-layers --location fsn1
```

You'll get an endpoint like `fsn1.your-objectstorage.com`. Generate
S3 credentials in the Hetzner console. cfunc's layerstore package
already speaks this — wiring into builder/gateway lands with 0.3.

## 3. Provision the gateway nodes

```sh
for n in a b c; do
  hcloud server create \
    --name cfunc-gw-$n \
    --type cx22 \
    --location fsn1 \
    --image ubuntu-24.04 \
    --ssh-key your-key
done
```

On each node, install runc and pull the image:

```sh
apt-get update && apt-get install -y runc docker.io
docker pull ghcr.io/fabianringel/cfunc:0.2.1
```

## 4. Configure DNS + ACME

Point `fn.example.org` (and a wildcard if you want path-style routing
later) at the Hetzner Load Balancer's IP. cfunc handles ACME itself —
DNS-01 via the Hetzner provider needs no port-80 exposure:

```sh
docker run -d --name cfunc-gateway --restart=always \
  --network host \
  -e HETZNER_DNS_API_TOKEN="$HETZNER_DNS_TOKEN" \
  -v /etc/cfunc:/etc/cfunc:ro \
  -v /var/lib/cfunc:/var/lib/cfunc \
  ghcr.io/fabianringel/cfunc:0.2.1 \
  cfunc-gateway \
    -addr=:443 \
    -tls-domain="fn.example.org,*.fn.example.org" \
    -tls-email=ops@example.org \
    -tls-dns-provider=hetzner \
    -admin-addr=127.0.0.1:8081 \
    -admin-token-file=/etc/cfunc/admin.token \
    -state-dsn="$CFUNC_STATE_DSN"
```

Repeat on `cfunc-gw-b` and `cfunc-gw-c`.

## 5. Initialise the schema (once)

From your workstation or any one node:

```sh
docker run --rm ghcr.io/fabianringel/cfunc:0.2.1 \
  cfunc cluster init --dsn "$CFUNC_STATE_DSN"
```

## 6. Wire up the load balancer

```sh
hcloud load-balancer create --name cfunc-lb --type lb11 --location fsn1
hcloud load-balancer add-target cfunc-lb --server cfunc-gw-a
hcloud load-balancer add-target cfunc-lb --server cfunc-gw-b
hcloud load-balancer add-target cfunc-lb --server cfunc-gw-c
hcloud load-balancer add-service cfunc-lb \
  --protocol tcp --listen-port 443 --destination-port 443
```

cfunc gateways are stateless w.r.t. the public port; round-robin works.
Sticky session affinity is **not** required in 0.2 because state lives
in Postgres, but it'll improve cold-start latency for repeat callers
of the same function (page cache locality). Enable it once you have a
function-name → backend hash routing layer in place — that's a 0.4
deliverable.

## 7. Verify

```sh
docker run --rm ghcr.io/fabianringel/cfunc:0.2.1 \
  cfunc cluster status --dsn "$CFUNC_STATE_DSN"
```

Should print `(none)` for functions and crons until you deploy your
first one.

---

## Cost estimate (April 2026 prices)

| Component | Spec | Monthly |
|---|---|---|
| 3× cx22 gateway | 2 vCPU, 4 GB RAM, 40 GB SSD | €15.57 |
| Hetzner Postgres (small) | 1 vCPU, 2 GB | ~€8 |
| Object Storage | 50 GB + traffic | ~€2 |
| Load Balancer lb11 | 10k connections | €5.83 |
| **Total** | | **~€31** |

For comparison, the equivalent AWS Lambda + RDS + S3 setup with the
same throughput sits around €180/month, plus egress charges and
a non-EU jurisdiction.

## Operations

- **Logs:** `docker logs -f cfunc-gateway` on each node, or pipe to your
  log aggregator. The gateway emits structured JSON via slog.
- **Metrics:** dashboard at `https://gw-a.internal:8081/_/` (admin
  port, token-protected). Prometheus exporter is a 0.6 deliverable.
- **Upgrades:** the schema is forward-compatible within a minor; for
  0.x → 0.y read the release notes. Rolling update one node at a time
  works because state lives in Postgres.
- **Backups:** Hetzner Postgres backups cover the state. Object Storage
  is content-addressed — losing a layer means re-running the builder.

## Failure modes

- **Postgres down:** gateways keep serving from local cache; new
  function registrations fail until Postgres returns. Cron stops firing
  (no leader election possible).
- **One gateway down:** load balancer routes traffic to the others.
  Their warm pools take the slack. Cold-starts may briefly spike.
- **Builder down:** existing functions keep running; new layer builds
  fail until the builder returns.
