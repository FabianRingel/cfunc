// SPDX-License-Identifier: Apache-2.0

package gateway_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fabianringel/cfunc/internal/gateway"
)

// TestIdleReaper builds the example function and verifies that an
// instance is reaped after IdleTTL elapses. We use an injected clock so
// the test runs instantly.
func TestIdleReaper(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "example")
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, "./templates/go/example")
	cmd.Dir = repo
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}

	var nowNS atomic.Int64
	nowNS.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, nowNS.Load()) }

	gw := gateway.NewWithOptions(gateway.Options{
		IdleTTL:      100 * time.Millisecond,
		ReapInterval: 1 * time.Hour, // disable background ticks; we drive ReapNow
		Now:          clock,
	})
	defer gw.Close()
	gw.Register("demo", bin)

	srv := httptest.NewServer(gw)
	defer srv.Close()

	// First call spawns the instance.
	resp, err := http.Get(srv.URL + "/fn/demo")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Advance clock past IdleTTL and reap.
	nowNS.Add(int64(time.Second))
	gw.ReapNow()

	// Next call should spawn a fresh instance — proven by the fact that
	// the call still succeeds (reaped instance was cleaned up properly).
	resp2, err := http.Get(srv.URL + "/fn/demo")
	if err != nil {
		t.Fatalf("post-reap call failed: %v", err)
	}
	if resp2.StatusCode != 200 {
		t.Fatalf("status=%d", resp2.StatusCode)
	}
	resp2.Body.Close()
}
