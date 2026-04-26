// Package cfunc is the Go SDK for writing cfunc functions.
//
// A function binary calls Start(handler). The SDK connects to the Unix
// socket given via env CFUNC_SOCKET, reads invoke frames, dispatches to
// the user handler, and writes result/error frames back.
package cfunc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"time"

	"github.com/fabianringel/cfunc/internal/wire"
)

// Event is the inbound payload passed to a handler.
type Event struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// Response is what a handler returns.
type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// Handler is the user function signature.
type Handler func(ctx context.Context, event Event) (Response, error)

// Start blocks, serving frames on the socket from CFUNC_SOCKET until EOF.
func Start(h Handler) error {
	sock := os.Getenv("CFUNC_SOCKET")
	if sock == "" {
		return errors.New("cfunc: CFUNC_SOCKET not set")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return fmt.Errorf("cfunc: dial %s: %w", sock, err)
	}
	defer conn.Close()
	return Serve(conn, h)
}

// Serve runs the frame loop on rw. Exposed for testing.
func Serve(rw io.ReadWriter, h Handler) error {
	for {
		f, err := wire.ReadFrame(rw)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch f.Type {
		case wire.TypeInvoke:
			handleInvoke(rw, h, f)
		case wire.TypeShutdown:
			_ = wire.WriteFrame(rw, &wire.Frame{Type: wire.TypeShutdownOK, ID: f.ID})
			return nil
		case wire.TypeInit:
			_ = wire.WriteFrame(rw, &wire.Frame{Type: wire.TypeInitOK, ID: f.ID})
		default:
			_ = wire.WriteFrame(rw, &wire.Frame{
				Type: wire.TypeError, ID: f.ID,
				Error: &wire.ErrorPayload{Type: "ProtocolError", Message: "unknown frame type: " + string(f.Type)},
			})
		}
	}
}

func handleInvoke(rw io.Writer, h Handler, f *wire.Frame) {
	ctx, cancel := contextFor(f.Ctx)
	defer cancel()

	var event Event
	if len(f.Event) > 0 {
		if err := json.Unmarshal(f.Event, &event); err != nil {
			writeErr(rw, f.ID, "BadEvent", err.Error(), "")
			return
		}
	}

	resp, err := safeCall(ctx, h, event)
	if err != nil {
		writeErr(rw, f.ID, "HandlerError", err.Error(), "")
		return
	}
	body, err := json.Marshal(resp)
	if err != nil {
		writeErr(rw, f.ID, "MarshalError", err.Error(), "")
		return
	}
	_ = wire.WriteFrame(rw, &wire.Frame{Type: wire.TypeResult, ID: f.ID, Result: body})
}

func safeCall(ctx context.Context, h Handler, e Event) (resp Response, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return h(ctx, e)
}

func writeErr(w io.Writer, id, typ, msg, stack string) {
	_ = wire.WriteFrame(w, &wire.Frame{
		Type: wire.TypeError, ID: id,
		Error: &wire.ErrorPayload{Type: typ, Message: msg, Stack: stack},
	})
}

func contextFor(raw json.RawMessage) (context.Context, context.CancelFunc) {
	if len(raw) == 0 {
		return context.WithCancel(context.Background())
	}
	var c struct {
		DeadlineMS int64 `json:"deadline_ms"`
	}
	_ = json.Unmarshal(raw, &c)
	if c.DeadlineMS > 0 {
		return context.WithTimeout(context.Background(), time.Duration(c.DeadlineMS)*time.Millisecond)
	}
	return context.WithCancel(context.Background())
}
