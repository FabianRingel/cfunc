// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDashboardAuthSplit verifies that the auth-split middleware
// keeps static dashboard assets reachable without a token while
// still gating the API and WebSocket paths. Catches the regression
// where the React bundle couldn't load → no UI to enter the token
// in → operator locked out.
func TestDashboardAuthSplit(t *testing.T) {
	// next echoes the path so we can tell whether the request reached
	// the inner handler.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Reached", "1")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(r.URL.Path))
	})
	h := dashboardAuthSplit("/_/", "secret", next)
	srv := httptest.NewServer(h)
	defer srv.Close()

	cases := []struct {
		path        string
		withToken   bool
		wantStatus  int
		wantReached bool
	}{
		{"/_/", false, 200, true},                   // index html, unauthed
		{"/_/index.html", false, 200, true},         // ditto
		{"/_/assets/app.js", false, 200, true},      // bundle, unauthed
		{"/_/api/state", false, 401, false},         // API gated
		{"/_/api/functions", false, 401, false},     // API gated
		{"/_/ws", false, 401, false},                // WS gated
		{"/_/api/state", true, 200, true},           // API with token
		{"/_/ws", true, 200, true},                  // WS with token
		{"/somewhere-else", false, 401, false},      // non-dashboard path: gated
	}
	for _, c := range cases {
		req, _ := http.NewRequest("GET", srv.URL+c.path, nil)
		if c.withToken {
			req.Header.Set("Authorization", "Bearer secret")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.wantStatus {
			t.Errorf("%s (token=%v): status %d, want %d",
				c.path, c.withToken, resp.StatusCode, c.wantStatus)
		}
		reached := resp.Header.Get("X-Reached") == "1"
		if reached != c.wantReached {
			t.Errorf("%s (token=%v): reached=%v, want %v",
				c.path, c.withToken, reached, c.wantReached)
		}
	}
}

// Empty token disables the entire split — useful for loopback dev.
func TestDashboardAuthSplitEmptyTokenIsPassthrough(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(204)
	})
	h := dashboardAuthSplit("/_/", "", next)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/_/api/state")
	if resp.StatusCode != 204 || !called {
		t.Fatalf("passthrough broken: status=%d called=%v", resp.StatusCode, called)
	}
}
