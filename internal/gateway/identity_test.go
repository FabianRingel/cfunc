// SPDX-License-Identifier: Apache-2.0

package gateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/fabianringel/cfunc/internal/auth"
	"github.com/fabianringel/cfunc/internal/gateway"
	"github.com/fabianringel/cfunc/internal/spawn"
	"github.com/fabianringel/cfunc/internal/state"
)

// spySpawner returns an error and increments a counter. We never let
// the spawn complete because that would require a real binary, but
// spawnCalled tells us whether ServeHTTP got past the project check
// far enough to reach acquire.
func spySpawner(counter *int64) gateway.Spawner {
	return func(_ gateway.FunctionDef) (*spawn.Instance, error) {
		atomic.AddInt64(counter, 1)
		return nil, errors.New("spy spawner: refused")
	}
}

// TestServeHTTPRejectsCrossProjectIdentity proves that an authenticated
// identity scoped to project A cannot invoke a function registered to
// project B. The strong invariant: when the cross-project check fires,
// the spawner is never called. We assert on the spawner-call-count
// rather than on the HTTP status alone — that way the test can't pass
// "for the wrong reason" if some unrelated 5xx happens to surface.
func TestServeHTTPRejectsCrossProjectIdentity(t *testing.T) {
	store := state.NewInMemStore()
	_ = store.CreateProject(context.Background(), state.Project{Name: "owner"})

	var spawnCalls int64
	gw := gateway.NewWithOptions(gateway.Options{
		Store: store,
		Spawn: spySpawner(&spawnCalls),
	})
	defer gw.Close()

	if err := gw.RegisterDef(gateway.FunctionDef{
		Name: "victim", Binary: "/usr/bin/true", Project: "owner",
	}); err != nil {
		t.Fatal(err)
	}

	withIdentity := func(id auth.Identity) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gw.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
		}))
	}

	intruder := withIdentity(auth.Identity{
		KeyID: "ck_intruder", Project: "intruder", Scopes: []string{"invoke"},
	})
	defer intruder.Close()

	for _, path := range []string{"/fn/victim", "/v1/owner/fn/victim"} {
		atomic.StoreInt64(&spawnCalls, 0)
		resp, err := http.Get(intruder.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: cross-project identity should get 404, got %d", path, resp.StatusCode)
		}
		if got := atomic.LoadInt64(&spawnCalls); got != 0 {
			t.Errorf("%s: spawner was called %d times — project check did not fire", path, got)
		}
	}

	// Same-project identity: project check passes, so the gateway
	// proceeds to acquire and the spy spawner gets called. We don't
	// care about the resulting HTTP status — only that spawn was
	// reached, which is the inverse evidence that the cross-project
	// check is what stopped the intruder above.
	owner := withIdentity(auth.Identity{
		KeyID: "ck_owner", Project: "owner", Scopes: []string{"invoke"},
	})
	defer owner.Close()
	atomic.StoreInt64(&spawnCalls, 0)
	resp, err := http.Get(owner.URL + "/v1/owner/fn/victim")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt64(&spawnCalls); got == 0 {
		t.Errorf("same-project identity: spawner not reached — project check incorrectly rejected")
	}
}

// TestServeHTTPRoutesV1Path proves that the gateway's ServeHTTP
// accepts the multi-tenant URL form. Catches the regression where
// the public mux only routed /fn/ and silently 404'd /v1/*.
func TestServeHTTPRoutesV1Path(t *testing.T) {
	store := state.NewInMemStore()
	_ = store.CreateProject(context.Background(), state.Project{Name: "acme"})

	var spawnCalls int64
	gw := gateway.NewWithOptions(gateway.Options{
		Store: store,
		Spawn: spySpawner(&spawnCalls),
	})
	defer gw.Close()
	if err := gw.RegisterDef(gateway.FunctionDef{
		Name: "fn", Binary: "/usr/bin/true", Project: "acme",
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/acme/fn/fn")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if atomic.LoadInt64(&spawnCalls) == 0 {
		t.Errorf("/v1/<project>/fn/<name> did not reach spawner — routing dropped the request")
	}
}

// TestServeHTTPNoIdentitySkipsCrossProjectCheck ensures the legacy
// single-tenant deployment (no auth middleware) keeps working: a
// request with no identity in the context proceeds past the project
// check and reaches the spawner.
func TestServeHTTPNoIdentitySkipsCrossProjectCheck(t *testing.T) {
	store := state.NewInMemStore()
	_ = store.CreateProject(context.Background(), state.Project{Name: "owner"})

	var spawnCalls int64
	gw := gateway.NewWithOptions(gateway.Options{
		Store: store,
		Spawn: spySpawner(&spawnCalls),
	})
	defer gw.Close()
	if err := gw.RegisterDef(gateway.FunctionDef{
		Name: "fn", Binary: "/usr/bin/true", Project: "owner",
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/fn/fn")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if atomic.LoadInt64(&spawnCalls) == 0 {
		t.Error("no-identity request should reach spawner; project check fired incorrectly")
	}
}
