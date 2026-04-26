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
)

// TestPythonLayerSubprocess proves the Python layer-builder pipeline
// end-to-end without requiring a Python-bearing container rootfs:
//
//  1. pip install a tiny package into a temp dir
//  2. Register the dir as a cfunc layer
//  3. Resolve the layer's host path
//  4. Spawn the uses-layer handler with PYTHONPATH including the host path
//  5. Invoke and confirm the handler successfully imported from the layer
//
// Mirrors what runc-mode would do, just with the bind-mount substituted
// by a PYTHONPATH entry — the layer-builder logic is identical.
func TestPythonLayerSubprocess(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed: " + err.Error())
	}
	if err := exec.Command("python3", "-m", "pip", "--version").Run(); err != nil {
		t.Skip("python3 -m pip unavailable: " + err.Error())
	}
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	// 1. pip install into a layer source dir.
	layerSrc := t.TempDir()
	if err := os.Chmod(layerSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	pip := exec.Command("python3", "-m", "pip", "install",
		"--target", layerSrc, "six==1.16.0",
		"--no-compile", "--disable-pip-version-check", "--quiet")
	pip.Stdout, pip.Stderr = os.Stdout, os.Stderr
	if err := pip.Run(); err != nil {
		t.Fatalf("pip install: %v", err)
	}

	// 2. Register as a cfunc layer.
	store, err := layers.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("pylib", "1.0", "/opt/layers/pylib", "python-3", layerSrc); err != nil {
		t.Fatal(err)
	}
	_, hostPath, err := store.Resolve(layers.Ref{Name: "pylib", Version: "1.0"})
	if err != nil {
		t.Fatal(err)
	}

	// 3. Spawn the handler with PYTHONPATH = SDK + layer host path.
	sdkDir := filepath.Join(repo, "sdks", "python")
	handler := filepath.Join(repo, "templates", "python", "uses-layer", "handler.py")

	gw := gateway.New()
	defer gw.Close()
	gw.RegisterDef(gateway.FunctionDef{
		Name:   "py-layer",
		Binary: handler,
		Env: []string{
			"PYTHONPATH=" + sdkDir + ":" + hostPath,
			"LAYER_PATH=" + hostPath,
		},
	})

	srv := httptest.NewServer(gw)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/fn/py-layer")
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
	if got["six_version"] != "1.16.0" {
		t.Fatalf("layer module not imported correctly: %v", got)
	}
	t.Logf("layer-imported six %s from %s", got["six_version"], got["layer_path"])
}
