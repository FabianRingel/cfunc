// SPDX-License-Identifier: Apache-2.0

package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fabianringel/cfunc/internal/auth"
	"github.com/fabianringel/cfunc/internal/gateway"
	"github.com/fabianringel/cfunc/internal/state"
)

// TestServeHTTPRejectsCrossProjectIdentity proves that an authenticated
// identity scoped to project A cannot invoke a function registered to
// project B, regardless of which routing form is used.
func TestServeHTTPRejectsCrossProjectIdentity(t *testing.T) {
	store := state.NewInMemStore()
	_ = store.CreateProject(context.Background(), state.Project{Name: "owner"})

	gw := gateway.NewWithOptions(gateway.Options{Store: store})
	defer gw.Close()

	// Register a function owned by project "owner" via the store
	// directly, then wait for the gateway's Watch loop to ingest it.
	if err := gw.RegisterDef(gateway.FunctionDef{
		Name: "victim", Binary: "/usr/bin/true", Project: "owner",
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject an "intruder" identity (scope: invoke on project "intruder")
		// before the gateway sees the request.
		id := auth.Identity{KeyID: "ck_intruder", Project: "intruder", Scopes: []string{"invoke"}}
		gw.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
	}))
	defer srv.Close()

	for _, path := range []string{"/fn/victim", "/v1/owner/fn/victim"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: cross-project identity should get 404, got %d", path, resp.StatusCode)
		}
	}

	// Sanity: an identity scoped to "owner" passes the project check.
	// (We don't actually spawn /usr/bin/true here because that path
	// triggers the spawn machinery; the project-check rejection runs
	// before spawn, so 404 means the check fired.)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := auth.Identity{KeyID: "ck_owner", Project: "owner", Scopes: []string{"invoke"}}
		gw.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
	}))
	defer srv2.Close()

	resp, err := http.Get(srv2.URL + "/v1/owner/fn/victim")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// /usr/bin/true exists on Linux test hosts; on macOS the spawn might
	// fail with a different status. We only care that the response is
	// NOT 404, which would indicate the cross-project check rejected it.
	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("same-project identity should pass project check, got 404")
	}
}

// TestServeHTTPNoIdentitySkipsCrossProjectCheck ensures the legacy
// single-tenant deployment (no auth middleware) keeps working: a
// request with no identity in the context isn't blocked.
func TestServeHTTPNoIdentitySkipsCrossProjectCheck(t *testing.T) {
	store := state.NewInMemStore()
	_ = store.CreateProject(context.Background(), state.Project{Name: "owner"})
	gw := gateway.NewWithOptions(gateway.Options{Store: store})
	defer gw.Close()

	if err := gw.RegisterDef(gateway.FunctionDef{
		Name: "fn", Binary: "/usr/bin/true", Project: "owner",
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/fn/fn")
	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("no-identity request should not 404 on cross-project check")
	}
	resp.Body.Close()
}
