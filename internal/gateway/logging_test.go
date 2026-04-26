// SPDX-License-Identifier: Apache-2.0

package gateway_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabianringel/cfunc/internal/gateway"
)

// TestLoggingEmitsColdStartAndInvoke captures gateway log output and
// asserts the two structured events the operator relies on:
// "spawned" with cold_start_ms, and "invoke" with duration_ms.
func TestLoggingEmitsColdStartAndInvoke(t *testing.T) {
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

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	gw := gateway.NewWithOptions(gateway.Options{Logger: logger})
	defer gw.Close()
	gw.Register("demo", bin)

	srv := httptest.NewServer(gw)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/fn/demo")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	var sawSpawn, sawInvoke bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("non-JSON log line: %q", line)
		}
		switch rec["msg"] {
		case "spawned":
			if rec["fn"] != "demo" {
				t.Errorf("spawned: fn=%v", rec["fn"])
			}
			if _, ok := rec["cold_start_ms"]; !ok {
				t.Errorf("spawned: missing cold_start_ms: %v", rec)
			}
			if rec["mode"] != "process" {
				t.Errorf("spawned: mode=%v", rec["mode"])
			}
			sawSpawn = true
		case "invoke":
			if _, ok := rec["duration_ms"]; !ok {
				t.Errorf("invoke: missing duration_ms: %v", rec)
			}
			if _, ok := rec["request_id"]; !ok {
				t.Errorf("invoke: missing request_id: %v", rec)
			}
			sawInvoke = true
		}
	}
	if !sawSpawn {
		t.Error("no 'spawned' log emitted")
	}
	if !sawInvoke {
		t.Error("no 'invoke' log emitted")
	}
}
