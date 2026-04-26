# cfunc — convenience targets

.PHONY: dashboard build test test-runc test-runc-e2e test-runc-share lima-up lima-down clean

# Re-build the React dashboard bundle into internal/dashboard/web/dist.
# Required after editing anything under internal/dashboard/web/src.
dashboard:
	cd internal/dashboard/web && npm install --silent && npm run build

# Build all Go binaries.
build:
	go build ./...

# Run all platform-independent tests on the host.
test:
	go test ./cmd/... ./internal/... ./sdks/...

# Container-mode tests; require the Lima VM.
test-runc:
	./scripts/lima-setup.sh test-runc

test-runc-e2e:
	./scripts/lima-setup.sh test-runc-e2e

test-runc-share:
	./scripts/lima-setup.sh test-runc-share

lima-up:
	./scripts/lima-setup.sh up

lima-down:
	./scripts/lima-setup.sh destroy

clean:
	rm -rf internal/dashboard/web/dist internal/dashboard/web/node_modules
