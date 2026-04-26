package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/fabianringel/cfunc/internal/gateway"
)

// TestE2E_Python proves the Phase-5 multi-language path: a Python handler
// is spawned as a subprocess by the same gateway code, talks the same
// wire protocol, and returns a response.
//
// Skipped if python3 is not on PATH.
func TestE2E_Python(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed: " + err.Error())
	}
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	handler := filepath.Join(repo, "templates", "python", "example", "handler.py")
	sdkDir := filepath.Join(repo, "sdks", "python")

	gw := gateway.New()
	defer gw.Close()
	gw.RegisterDef(gateway.FunctionDef{
		Name:   "py-demo",
		Binary: handler,
		Env:    []string{"PYTHONPATH=" + sdkDir},
	})

	srv := httptest.NewServer(gw)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/fn/py-demo")
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
	if got["lang"] != "python" || got["hello"] != "world" {
		t.Fatalf("got %v", got)
	}
}
