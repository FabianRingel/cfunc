#!/usr/bin/env bash
# Bootstraps the Linux dev VM used by cfunc to run runc-backed tests.
#
# Idempotent: re-running starts the VM if it is stopped, no-ops if running.
#
#   ./scripts/lima-setup.sh             # ensure VM up
#   ./scripts/lima-setup.sh test        # run `go test ./...` inside the VM
#   ./scripts/lima-setup.sh shell       # interactive shell inside the VM
#   ./scripts/lima-setup.sh destroy     # remove the VM

set -euo pipefail

VM_NAME="cfunc"
CONFIG="$(cd "$(dirname "$0")" && pwd)/cfunc-dev.yaml"

require() {
  command -v "$1" >/dev/null || {
    echo "missing: $1" >&2
    [[ "$1" == "limactl" ]] && echo "  brew install lima" >&2
    exit 1
  }
}

ensure_up() {
  require limactl
  status=$(limactl list --format '{{.Status}}' "$VM_NAME" 2>/dev/null || true)
  case "$status" in
    Running)  ;;
    Stopped)  limactl start "$VM_NAME" ;;
    "")       limactl start --tty=false --name="$VM_NAME" "$CONFIG" ;;
    *)        echo "unexpected status: $status"; exit 1 ;;
  esac
}

case "${1:-up}" in
  up)
    ensure_up
    echo "VM '$VM_NAME' ready. limactl shell $VM_NAME"
    ;;
  test)
    ensure_up
    limactl shell "$VM_NAME" -- bash -lc "cd '$PWD' && go test ./..."
    ;;
  test-runc)
    ensure_up
    limactl shell "$VM_NAME" -- bash -lc "cd '$PWD' && go test -tags=runc_integration ./internal/runc/..."
    ;;
  test-runc-e2e)
    ensure_up
    limactl shell "$VM_NAME" -- bash -lc "cd '$PWD' && go test -tags=runc_integration -v -run E2E_Runc ./internal/gateway/..."
    ;;
  test-runc-share)
    ensure_up
    limactl shell "$VM_NAME" -- bash -lc "cd '$PWD' && go test -tags=runc_integration -v -run LayerSharing ./internal/gateway/..."
    ;;
  shell)
    ensure_up
    limactl shell "$VM_NAME"
    ;;
  destroy)
    require limactl
    limactl stop "$VM_NAME" 2>/dev/null || true
    limactl delete "$VM_NAME"
    ;;
  *)
    echo "usage: $0 [up|test|test-runc|shell|destroy]" >&2
    exit 2
    ;;
esac
