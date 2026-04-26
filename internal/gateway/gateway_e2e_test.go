// SPDX-License-Identifier: Apache-2.0

package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabianringel/cfunc/internal/gateway"
)

// TestE2E builds the example function and exercises it through the gateway
// over real HTTP, validating the full Phase-1 path:
//
//	HTTP -> gateway -> spawn user binary -> unix socket -> SDK -> handler -> back.
func TestE2E(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "example")

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, "./templates/go/example")
	cmd.Dir = repoRoot
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build example: %v", err)
	}

	gw := gateway.New()
	gw.Register("demo", bin)
	defer gw.Close()

	srv := httptest.NewServer(gw)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/fn/demo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if got["hello"] != "world" {
		t.Fatalf("got %v", got)
	}

	// Second request reuses the spawned instance.
	resp2, err := http.Get(srv.URL + "/fn/demo")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("second call status=%d", resp2.StatusCode)
	}
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
	_ = strings.TrimSpace
	return "", nil
}
