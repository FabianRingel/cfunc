// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// wsMessage is the on-the-wire envelope for client-bound messages.
type wsMessage struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type helloPayload struct {
	Backlog []LogEvent `json:"backlog"`
}

// stateInterval is how often a state snapshot is pushed to each client.
// Cheap snapshot + tiny payload, so 1 s feels live without being noisy.
const stateInterval = 1 * time.Second

func (h *Handler) serveWS(w http.ResponseWriter, r *http.Request) {
	opts := &websocket.AcceptOptions{}
	// Same-origin by default. Operators opt in to other origins via
	// -allowed-origins (CSV) → AllowedOrigins. The literal "*" is the
	// only way to fully disable the check (for reverse-proxy setups
	// where Origin is rewritten); we don't want that to be the default.
	for _, o := range h.allowedOrigins {
		if o == "*" {
			opts.InsecureSkipVerify = true
			break
		}
	}
	if !opts.InsecureSkipVerify {
		opts.OriginPatterns = h.allowedOrigins
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "closing")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 1. hello with current log backlog so the UI starts populated.
	if err := writeJSON(ctx, conn, wsMessage{
		Type: "hello",
		Data: helloPayload{Backlog: h.logs.Snapshot()},
	}); err != nil {
		return
	}
	// 2. immediate state snapshot.
	if err := writeJSON(ctx, conn, wsMessage{Type: "state", Data: h.stats.Stats()}); err != nil {
		return
	}

	// 3. live log subscription.
	logCh, unsub := h.logs.Subscribe()
	defer unsub()

	stateTicker := time.NewTicker(stateInterval)
	defer stateTicker.Stop()
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// 4. drain any client frames in the background; we don't expect any
	//    but a stuck reader breaks the spec.
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-logCh:
			if !ok {
				return
			}
			if err := writeJSON(ctx, conn, wsMessage{Type: "log", Data: ev}); err != nil {
				return
			}
		case <-stateTicker.C:
			if err := writeJSON(ctx, conn, wsMessage{Type: "state", Data: h.stats.Stats()}); err != nil {
				return
			}
		case <-pingTicker.C:
			pingCtx, pcancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

func writeJSON(ctx context.Context, conn *websocket.Conn, m wsMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}
