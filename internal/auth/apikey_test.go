// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fabianringel/cfunc/internal/state"
)

func okEcho(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := FromContext(r.Context())
		if id.KeyID == "" {
			t.Errorf("handler reached without identity")
		}
		w.Header().Set("X-Key-ID", id.KeyID)
		w.Header().Set("X-Project", id.Project)
		w.WriteHeader(200)
	})
}

func TestAPIKeyAuthAdminTokenPath(t *testing.T) {
	h := APIKeyAuth("admintok", nil, "", okEcho(t))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer admintok")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("admin path got %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Key-ID") != "admin-token" {
		t.Fatalf("admin identity not propagated: %q", resp.Header.Get("X-Key-ID"))
	}
}

func TestAPIKeyAuthAPIKeyPath(t *testing.T) {
	store := state.NewInMemStore()
	ctx := context.Background()
	_ = store.CreateProject(ctx, state.Project{Name: "acme"})
	plain := "ck_plain_secret"
	tok := sha256.Sum256([]byte(plain))
	_ = store.CreateAPIKey(ctx, state.APIKey{
		ID: "ck_test", Project: "acme", TokenSHA256: tok[:],
		Scopes: []string{"deploy"},
	})

	h := APIKeyAuth("", store, "deploy", okEcho(t))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("api key path got %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Project") != "acme" {
		t.Fatalf("project not propagated: %q", resp.Header.Get("X-Project"))
	}
}

func TestAPIKeyAuthMissingScopeForbidden(t *testing.T) {
	store := state.NewInMemStore()
	ctx := context.Background()
	_ = store.CreateProject(ctx, state.Project{Name: "acme"})
	tok := sha256.Sum256([]byte("k"))
	_ = store.CreateAPIKey(ctx, state.APIKey{
		ID: "ck", Project: "acme", TokenSHA256: tok[:],
		Scopes: []string{"invoke"}, // no deploy
	})

	h := APIKeyAuth("", store, "deploy",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("forbidden request should not reach handler")
		}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer k")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d, want 403", resp.StatusCode)
	}
}

func TestAPIKeyAuthRejectsUnknownToken(t *testing.T) {
	store := state.NewInMemStore()
	h := APIKeyAuth("admintok", store, "",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("unauth request should not reach handler")
		}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", resp.StatusCode)
	}
}

func TestAPIKeyAuthPassthroughWhenBothEmpty(t *testing.T) {
	called := false
	h := APIKeyAuth("", nil, "",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(204)
		}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, _ := http.Get(srv.URL)
	if resp.StatusCode != 204 || !called {
		t.Fatalf("passthrough broken: status=%d called=%v", resp.StatusCode, called)
	}
}

func TestAPIKeyAuthAdminBypassesScope(t *testing.T) {
	// admin-token must pass any required scope check.
	h := APIKeyAuth("adm", nil, "deploy", okEcho(t))
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer adm")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("admin should bypass scope check, got %d", resp.StatusCode)
	}
}
