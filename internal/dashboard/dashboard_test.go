// SPDX-License-Identifier: Apache-2.0

package dashboard_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/fabianringel/cfunc/internal/dashboard"
)

type stubStats struct{ payload any }

func (s stubStats) Stats() any { return s.payload }

func newCapture() *dashboard.LogCapture {
	return dashboard.NewLogCapture(slog.NewTextHandler(io.Discard, nil), 100)
}

func TestServesIndex(t *testing.T) {
	d := dashboard.New("/_/", stubStats{nil}, newCapture())
	srv := httptest.NewServer(d)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/_/")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "<title>cfunc dashboard</title>") {
		t.Fatalf("index missing title: %q", body)
	}
}

func TestServesStateAPI(t *testing.T) {
	want := map[string]any{"functions": []any{map[string]any{"name": "x"}}}
	d := dashboard.New("/_/", stubStats{want}, newCapture())
	srv := httptest.NewServer(d)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/_/api/state")
	defer resp.Body.Close()
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	if fns, _ := got["functions"].([]any); len(fns) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestLogCaptureRingBufferSnapshot(t *testing.T) {
	cap := dashboard.NewLogCapture(slog.NewTextHandler(io.Discard, nil), 3)
	logger := slog.New(cap)
	logger.Info("a")
	logger.Info("b")
	logger.Info("c")
	logger.Info("d") // should evict "a"

	snap := cap.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("len=%d", len(snap))
	}
	if snap[0].Message != "b" || snap[2].Message != "d" {
		t.Fatalf("got %+v", snap)
	}
}

func TestLogCaptureForwardsToDownstream(t *testing.T) {
	var buf bytes.Buffer
	cap := dashboard.NewLogCapture(slog.NewTextHandler(&buf, nil), 10)
	slog.New(cap).Info("hello", "k", 1)
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("downstream missing: %q", buf.String())
	}
}

func TestLogCaptureSubscribeLive(t *testing.T) {
	cap := dashboard.NewLogCapture(slog.NewTextHandler(io.Discard, nil), 10)
	ch, cancel := cap.Subscribe()
	defer cancel()
	go slog.New(cap).Info("ping")

	select {
	case ev := <-ch:
		if ev.Message != "ping" {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event delivered")
	}
}

// TestWebSocketStreamsHelloStateAndLogs verifies the live WebSocket
// pushes the three message kinds the React UI consumes:
//   - hello (with backlog)
//   - state (initial snapshot)
//   - log (live event)
func TestWebSocketStreamsHelloStateAndLogs(t *testing.T) {
	cap := newCapture()
	logger := slog.New(cap)
	logger.Info("backlog-event")

	d := dashboard.New("/_/", stubStats{map[string]any{"functions": []any{}}}, cap)
	srv := httptest.NewServer(d)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/_/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// hello
	var msg map[string]any
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if msg["type"] != "hello" {
		t.Fatalf("first frame not hello: %+v", msg)
	}

	// state
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if msg["type"] != "state" {
		t.Fatalf("second frame not state: %+v", msg)
	}

	// trigger live log and expect it within a couple of seconds
	logger.Info("live-event")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatalf("read log: %v", err)
		}
		if msg["type"] == "log" {
			data := msg["data"].(map[string]any)
			if data["message"] == "live-event" {
				return
			}
		}
	}
	t.Fatal("live event not received")
}
