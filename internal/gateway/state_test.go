// SPDX-License-Identifier: Apache-2.0

package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fabianringel/cfunc/internal/gateway"
	"github.com/fabianringel/cfunc/internal/state"
)

// TestGatewayLoadsExistingFunctionsFromStore proves that a gateway
// constructed against a populated store sees those functions on
// startup, without needing in-process RegisterDef calls. This is the
// foundational property for cluster mode: a fresh replica restarts
// against a shared state store and immediately knows what to serve.
func TestGatewayLoadsExistingFunctionsFromStore(t *testing.T) {
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := buildExample(t, repo)

	store := state.NewInMemStore()
	defer store.Close()
	if err := store.PutFunction(context.Background(), state.FunctionDef{
		Name: "preloaded", Binary: bin,
	}); err != nil {
		t.Fatal(err)
	}

	gw := gateway.NewWithOptions(gateway.Options{Store: store})
	defer gw.Close()
	srv := httptest.NewServer(gw)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/fn/preloaded")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d (gateway didn't load preloaded function)", resp.StatusCode)
	}
}

// TestGatewayReactsToStoreNotifications proves that a function added
// to the store *after* the gateway starts becomes routable within a
// short window — without anyone calling gateway.RegisterDef directly.
// This is what makes a multi-replica deployment work: replica B calls
// PutFunction, replica A picks it up via the Watch channel.
func TestGatewayReactsToStoreNotifications(t *testing.T) {
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := buildExample(t, repo)

	store := state.NewInMemStore()
	defer store.Close()

	gw := gateway.NewWithOptions(gateway.Options{Store: store})
	defer gw.Close()
	srv := httptest.NewServer(gw)
	defer srv.Close()

	// Initially the function is unknown.
	resp, _ := http.Get(srv.URL + "/fn/late")
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("function should not exist yet")
	}

	// Add via the store, not via gateway.RegisterDef.
	if err := store.PutFunction(context.Background(), state.FunctionDef{
		Name: "late", Binary: bin,
	}); err != nil {
		t.Fatal(err)
	}

	// Wait briefly for the gateway's watcher to apply the update.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(srv.URL + "/fn/late")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return // success
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("gateway did not pick up store notification within 2s")
}

// TestGatewayReactsToStoreDelete proves the symmetric path: deleting a
// function via the store removes it from the gateway's routable set.
func TestGatewayReactsToStoreDelete(t *testing.T) {
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := buildExample(t, repo)

	store := state.NewInMemStore()
	defer store.Close()
	_ = store.PutFunction(context.Background(), state.FunctionDef{Name: "doomed", Binary: bin})

	gw := gateway.NewWithOptions(gateway.Options{Store: store})
	defer gw.Close()
	srv := httptest.NewServer(gw)
	defer srv.Close()

	// Confirm function exists initially.
	resp, _ := http.Get(srv.URL + "/fn/doomed")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("preloaded function should be live")
	}

	// Delete via store.
	_ = store.DeleteFunction(context.Background(), "doomed")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, _ := http.Get(srv.URL + "/fn/doomed")
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("gateway did not drop function after store delete")
}

// TestGatewayRegisterDefStillWorks ensures the existing public API —
// RegisterDef called directly on the gateway — keeps working and
// writes through to the store.
func TestGatewayRegisterDefStillWorks(t *testing.T) {
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := buildExample(t, repo)

	store := state.NewInMemStore()
	defer store.Close()

	gw := gateway.NewWithOptions(gateway.Options{Store: store})
	defer gw.Close()

	if err := gw.RegisterDef(gateway.FunctionDef{Name: "via-api", Binary: bin}); err != nil {
		t.Fatal(err)
	}

	// Function must be in the store.
	got, err := store.GetFunction(context.Background(), "via-api")
	if err != nil {
		t.Fatalf("RegisterDef did not write to store: %v", err)
	}
	if got.Binary != bin {
		t.Fatalf("got %+v", got)
	}
}
