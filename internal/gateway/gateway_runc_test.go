// SPDX-License-Identifier: Apache-2.0

//go:build linux && runc_integration

package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/fabianringel/cfunc/internal/gateway"
	"github.com/fabianringel/cfunc/internal/oci"
	"github.com/fabianringel/cfunc/internal/runc"
	"github.com/fabianringel/cfunc/internal/spawn"
)

// TestE2E_Runc proves the full Phase-2 path:
// HTTP -> Gateway -> runc-backed container -> SDK -> handler -> back.
//
// Run via: ./scripts/lima-setup.sh test-runc-e2e
func TestE2E_Runc(t *testing.T) {
	if !runc.Available() {
		t.Skip("runc not installed")
	}
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	// runc inside the container needs the bind-mount source traversable by
	// the container's root; Go's t.TempDir defaults to 0700 which AppArmor
	// on Ubuntu 24.04 then blocks. Loosen to 0755 just for this directory.
	if err := os.Chmod(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "example")
	cmd := exec.Command("go", "build", "-o", bin, "./templates/go/example")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}

	rootfs, err := spawn.EnsureBaseRootfs("")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(rootfs)

	gw := gateway.NewWithOptions(gateway.Options{
		Spawn: func(def gateway.FunctionDef) (*spawn.Instance, error) {
			layers := make([]oci.Layer, 0, len(def.Layers))
			for _, l := range def.Layers {
				layers = append(layers, oci.Layer{
					Name: l.Name, HostPath: l.HostPath, MountPath: l.MountPath,
				})
			}
			return spawn.StartRunc(def.Binary, nil, spawn.RuncOptions{
				RootfsBase:  rootfs,
				Sudo:        os.Geteuid() != 0,
				ExtraLayers: layers,
			})
		},
	})
	defer gw.Close()
	gw.Register("demo", bin)

	srv := httptest.NewServer(gw)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/fn/demo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body=%q: %v", body, err)
	}
	if got["hello"] != "world" {
		t.Fatalf("got %v", got)
	}

	// Second call reuses the running container.
	resp2, _ := http.Get(srv.URL + "/fn/demo")
	if resp2.StatusCode != 200 {
		t.Fatalf("second call status=%d", resp2.StatusCode)
	}
	resp2.Body.Close()
}
