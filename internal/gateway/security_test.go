package gateway_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabianringel/cfunc/internal/gateway"
)

func buildExample(t *testing.T, repo string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "example")
	cmd := exec.Command("go", "build", "-o", bin, "./templates/go/example")
	cmd.Dir = repo
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build example: %v", err)
	}
	return bin
}

func TestRejectsInvalidFunctionNamesOnRegister(t *testing.T) {
	gw := gateway.New()
	defer gw.Close()

	cases := []string{
		"",
		"with/slash",
		"with space",
		"new\nline",
		"\x1b[2J",         // ANSI clear-screen
		"\x00",            // NUL
		strings.Repeat("a", 65), // too long
		".leading-dot",
		"-leading-dash",
	}
	for _, n := range cases {
		err := gw.RegisterDef(gateway.FunctionDef{Name: n, Binary: "/x"})
		if err == nil {
			t.Errorf("expected error for name %q", n)
		}
	}
}

func TestServeHTTP404sInvalidFunctionName(t *testing.T) {
	gw := gateway.New()
	defer gw.Close()
	srv := httptest.NewServer(gw)
	defer srv.Close()

	// URL-encoded ANSI clear: gateway should treat as 404 without
	// echoing the bytes anywhere (no log injection, no terminal damage).
	resp, err := http.Get(srv.URL + "/fn/%1B%5B2J")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestRequestBodySizeLimit(t *testing.T) {
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := buildExample(t, repo)

	gw := gateway.New()
	defer gw.Close()
	if err := gw.RegisterDef(gateway.FunctionDef{Name: "echo", Binary: bin}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw)
	defer srv.Close()

	// Just over the MaxRequestBody (8 MiB) cap.
	huge := bytes.Repeat([]byte("x"), gateway.MaxRequestBody+1024)
	resp, err := http.Post(srv.URL+"/fn/echo", "application/octet-stream", bytes.NewReader(huge))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
}
