package cfunc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/fabianringel/cfunc/internal/wire"
)

// pipeRW couples two byte streams as a duplex io.ReadWriter for Serve tests.
type pipeRW struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p *pipeRW) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeRW) Write(b []byte) (int, error) { return p.w.Write(b) }

func newDuplex() (client, server *pipeRW) {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	return &pipeRW{r: cr, w: cw}, &pipeRW{r: sr, w: sw}
}

func TestServeInvokeOK(t *testing.T) {
	client, server := newDuplex()
	h := func(ctx context.Context, e Event) (Response, error) {
		body, _ := json.Marshal(map[string]string{"echo": e.Path})
		return Response{Status: 200, Body: body}, nil
	}

	done := make(chan error, 1)
	go func() { done <- Serve(server, h) }()

	event, _ := json.Marshal(Event{Method: "GET", Path: "/x"})
	if err := wire.WriteFrame(client, &wire.Frame{Type: wire.TypeInvoke, ID: "1", Event: event}); err != nil {
		t.Fatal(err)
	}
	out, err := wire.ReadFrame(client)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != wire.TypeResult || out.ID != "1" {
		t.Fatalf("got %+v", out)
	}
	var resp Response
	if err := json.Unmarshal(out.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Fatalf("status %d", resp.Status)
	}

	client.w.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestServeHandlerError(t *testing.T) {
	client, server := newDuplex()
	h := func(ctx context.Context, e Event) (Response, error) {
		return Response{}, errors.New("kapow")
	}
	go Serve(server, h)

	wire.WriteFrame(client, &wire.Frame{Type: wire.TypeInvoke, ID: "2"})
	out, _ := wire.ReadFrame(client)
	if out.Type != wire.TypeError || out.Error.Message != "kapow" {
		t.Fatalf("got %+v", out)
	}
}

func TestServeHandlerPanicCaught(t *testing.T) {
	client, server := newDuplex()
	h := func(ctx context.Context, e Event) (Response, error) {
		panic("oops")
	}
	go Serve(server, h)

	wire.WriteFrame(client, &wire.Frame{Type: wire.TypeInvoke, ID: "3"})
	out, _ := wire.ReadFrame(client)
	if out.Type != wire.TypeError {
		t.Fatalf("got %+v", out)
	}
}

func TestServeShutdown(t *testing.T) {
	client, server := newDuplex()
	done := make(chan error, 1)
	go func() {
		done <- Serve(server, func(ctx context.Context, e Event) (Response, error) {
			return Response{Status: 200}, nil
		})
	}()
	wire.WriteFrame(client, &wire.Frame{Type: wire.TypeShutdown, ID: "sd"})
	out, _ := wire.ReadFrame(client)
	if out.Type != wire.TypeShutdownOK {
		t.Fatalf("got %+v", out)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}
