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
	"github.com/fabianringel/cfunc/internal/layers"
	"github.com/fabianringel/cfunc/internal/oci"
	"github.com/fabianringel/cfunc/internal/runc"
	"github.com/fabianringel/cfunc/internal/spawn"
)

// TestLayerSharing_SameInode is the central proof-of-concept for cfunc's
// memory-efficiency claim:
//
// Two containers reference the same registered layer. They each stat a
// file inside that layer's mount and report its (device, inode). The
// inodes MUST match — that means both containers' bind-mounts resolve to
// the same host inode, which means the Linux page cache shares any page
// of that file between them.
//
// Run via: ./scripts/lima-setup.sh test-runc-share
func TestLayerSharing_SameInode(t *testing.T) {
	if !runc.Available() {
		t.Skip("runc not installed")
	}

	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	if err := os.Chmod(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "inspect")
	build := exec.Command("go", "build", "-o", bin, "./templates/go/inspect")
	build.Dir = repo
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build inspect: %v", err)
	}

	// Layer source.
	srcDir := t.TempDir()
	if err := os.Chmod(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := "shared-content-marker-42"
	if err := os.WriteFile(filepath.Join(srcDir, "data"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := layers.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("shared", "1.0", "/opt/layers/shared", "any", srcDir); err != nil {
		t.Fatal(err)
	}
	man, hostPath, err := store.Resolve(layers.Ref{Name: "shared", Version: "1.0"})
	if err != nil {
		t.Fatal(err)
	}

	rootfs, err := spawn.EnsureBaseRootfs("")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(rootfs)

	gw := gateway.NewWithOptions(gateway.Options{
		Spawn: func(def gateway.FunctionDef) (*spawn.Instance, error) {
			ls := make([]oci.Layer, 0, len(def.Layers))
			for _, l := range def.Layers {
				ls = append(ls, oci.Layer{Name: l.Name, HostPath: l.HostPath, MountPath: l.MountPath})
			}
			return spawn.StartRunc(def.Binary, nil, spawn.RuncOptions{
				RootfsBase: rootfs, Sudo: os.Geteuid() != 0, ExtraLayers: ls,
			})
		},
	})
	defer gw.Close()

	mount := gateway.LayerMount{Name: "shared", HostPath: hostPath, MountPath: man.MountPath}
	gw.RegisterDef(gateway.FunctionDef{Name: "a", Binary: bin, Layers: []gateway.LayerMount{mount}})
	gw.RegisterDef(gateway.FunctionDef{Name: "b", Binary: bin, Layers: []gateway.LayerMount{mount}})

	srv := httptest.NewServer(gw)
	defer srv.Close()

	resA := callInspect(t, srv.URL+"/fn/a")
	resB := callInspect(t, srv.URL+"/fn/b")

	if resA.Inode == 0 || resB.Inode == 0 {
		t.Fatalf("got zero inode: a=%+v b=%+v", resA, resB)
	}
	if resA.Inode != resB.Inode || resA.Device != resB.Device {
		t.Fatalf("layer NOT shared between containers:\n  a: dev=%d ino=%d\n  b: dev=%d ino=%d",
			resA.Device, resA.Inode, resB.Device, resB.Inode)
	}
	if resA.Content != want || resB.Content != want {
		t.Fatalf("content mismatch:\n  a=%q\n  b=%q\n  want=%q", resA.Content, resB.Content, want)
	}
	t.Logf("layer shared: dev=%d ino=%d content=%q", resA.Device, resA.Inode, resA.Content)
}

type inspectResult struct {
	Path    string `json:"path"`
	Inode   uint64 `json:"inode"`
	Device  uint64 `json:"device"`
	Size    int64  `json:"size"`
	Content string `json:"content"`
}

func callInspect(t *testing.T, url string) inspectResult {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var r inspectResult
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("parse %q: %v", body, err)
	}
	return r
}
