# cfunc

Container-isolierter, multi-sprachiger Cloud-Function-Runner mit
gemeinsam genutzten Dependency-Layers und Scale-to-Zero pro Function.

Siehe [PLAN.md](./PLAN.md) für Vision, Architektur und Roadmap.

## Status

**Phase 1** (Wire & lokaler Lauf, ohne Container) — abgeschlossen.

## Schnelltest

```sh
# Beispiel-Function bauen
go build -o /tmp/example ./templates/go/example

# Gateway starten
go run ./cmd/gateway -binary=/tmp/example -fn=demo &

# Function aufrufen
curl http://localhost:8080/fn/demo
# {"hello":"world","method":"GET","path":"/fn/demo"}
```

## Tests

```sh
go test ./...
```

Der E2E-Test in `internal/gateway/gateway_e2e_test.go` baut die
Beispiel-Function und übt den vollen Pfad HTTP → Gateway → Spawn →
Unix-Socket → SDK → Handler → zurück.
