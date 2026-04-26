// SPDX-License-Identifier: Apache-2.0

package dashboard_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fabianringel/cfunc/internal/dashboard"
)

type stubAdmin struct {
	mu         sync.Mutex
	registered []dashboard.RegisterRequest
	dropped    []string
	regErr     error
	missingFor map[string]bool
}

func (s *stubAdmin) RegisterFunction(req dashboard.RegisterRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.regErr != nil {
		return s.regErr
	}
	s.registered = append(s.registered, req)
	return nil
}

func (s *stubAdmin) UnregisterFunction(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.missingFor != nil && s.missingFor[name] {
		return false
	}
	s.dropped = append(s.dropped, name)
	return true
}

func newWithAdmin(admin dashboard.Admin) *httptest.Server {
	d := dashboard.NewWithAdmin("/_/", stubStats{nil}, admin,
		dashboard.NewLogCapture(slog.NewTextHandler(io.Discard, nil), 10))
	return httptest.NewServer(d)
}

func TestAdminRegister(t *testing.T) {
	admin := &stubAdmin{}
	srv := newWithAdmin(admin)
	defer srv.Close()

	body := `{"name":"hello","binary":"/usr/local/bin/hello","env":["FOO=bar"]}`
	resp, err := http.Post(srv.URL+"/_/api/functions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()

	if got["endpoint"] != "/fn/hello" {
		t.Fatalf("got %v", got)
	}
	if len(admin.registered) != 1 || admin.registered[0].Name != "hello" {
		t.Fatalf("admin saw %+v", admin.registered)
	}
}

// TestAdminRegisterForwardsProject catches the bug where
// RegisterRequest didn't carry the project field, so the JSON
// {"project": "..."} was silently dropped and every function
// landed in the default project.
func TestAdminRegisterForwardsProject(t *testing.T) {
	admin := &stubAdmin{}
	srv := newWithAdmin(admin)
	defer srv.Close()

	body := `{"name":"fn","binary":"/usr/local/bin/fn","project":"acme"}`
	resp, err := http.Post(srv.URL+"/_/api/functions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if len(admin.registered) != 1 {
		t.Fatalf("admin saw %d registrations", len(admin.registered))
	}
	if admin.registered[0].Project != "acme" {
		t.Fatalf("project not forwarded: got %q, want %q",
			admin.registered[0].Project, "acme")
	}
}

func TestAdminRegisterRejectsMissingFields(t *testing.T) {
	admin := &stubAdmin{}
	srv := newWithAdmin(admin)
	defer srv.Close()

	cases := []string{
		`{"binary":"/x"}`,                                  // no name
		`{"name":"x"}`,                                     // no binary
		`{"name":" ","binary":"/x"}`,                       // empty name (after trim)
		`{"name":"x","binary":"relative"}`,                 // not absolute
		`{"name":"x","binary":"/foo/../bar"}`,              // unclean
		`{"name":"x","binary":"/x","max_concurrency":-1}`,  // negative
		`{"name":"x","binary":"/x","max_concurrency":9999}`,// over ceiling
	}
	for i, body := range cases {
		resp, _ := http.Post(srv.URL+"/_/api/functions", "application/json", strings.NewReader(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("case %d: status=%d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestAdminRejectsBadJSON(t *testing.T) {
	srv := newWithAdmin(&stubAdmin{})
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/_/api/functions", "application/json", strings.NewReader("nope"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestAdminUnregister(t *testing.T) {
	admin := &stubAdmin{}
	srv := newWithAdmin(admin)
	defer srv.Close()

	req, _ := http.NewRequest("DELETE", srv.URL+"/_/api/functions/hello", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if len(admin.dropped) != 1 || admin.dropped[0] != "hello" {
		t.Fatalf("dropped=%v", admin.dropped)
	}
}

func TestAdminUnregisterMissingIs404(t *testing.T) {
	admin := &stubAdmin{missingFor: map[string]bool{"gone": true}}
	srv := newWithAdmin(admin)
	defer srv.Close()
	req, _ := http.NewRequest("DELETE", srv.URL+"/_/api/functions/gone", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestAdmin404IfDisabled(t *testing.T) {
	d := dashboard.New("/_/", stubStats{nil}, dashboard.NewLogCapture(slog.NewTextHandler(io.Discard, nil), 10))
	srv := httptest.NewServer(d)
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/_/api/functions", "application/json", bytes.NewReader([]byte(`{"name":"x","binary":"/x"}`)))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}
