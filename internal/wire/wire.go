// Package wire implements the cfunc IPC protocol: length-prefixed JSON frames
// over any io.Reader/io.Writer (typically a Unix socket).
//
// Frame layout: [4 bytes big-endian length N][N bytes JSON payload].
// Frames are sequential per connection — one in-flight invoke at a time.
package wire

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// FrameType enumerates the protocol's frame kinds. Values are stable API.
type FrameType string

const (
	TypeInit       FrameType = "init"
	TypeInitOK     FrameType = "init_ok"
	TypeInvoke     FrameType = "invoke"
	TypeResult     FrameType = "result"
	TypeError      FrameType = "error"
	TypeShutdown   FrameType = "shutdown"
	TypeShutdownOK FrameType = "shutdown_ok"
)

// MaxFrameSize caps a single frame to guard against malformed peers.
const MaxFrameSize = 16 * 1024 * 1024 // 16 MiB

// Frame is the on-wire envelope. Type and ID are required; the remaining
// fields are populated based on Type.
type Frame struct {
	Type   FrameType       `json:"type"`
	ID     string          `json:"id"`
	Event  json.RawMessage `json:"event,omitempty"`
	Ctx    json.RawMessage `json:"ctx,omitempty"`
	Config json.RawMessage `json:"config,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ErrorPayload   `json:"error,omitempty"`
}

// ErrorPayload carries handler-side failure details.
type ErrorPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
}

// WriteFrame encodes f as JSON and writes it length-prefixed to w.
func WriteFrame(w io.Writer, f *Frame) error {
	payload, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("wire: marshal: %w", err)
	}
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("wire: frame too large: %d > %d", len(payload), MaxFrameSize)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("wire: write header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("wire: write payload: %w", err)
	}
	return nil
}

// ReadFrame reads one length-prefixed JSON frame from r.
// Returns io.EOF cleanly when the peer closed without partial data.
func ReadFrame(r io.Reader) (*Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err // io.EOF passes through unwrapped
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return nil, fmt.Errorf("wire: zero-length frame")
	}
	if n > MaxFrameSize {
		return nil, fmt.Errorf("wire: frame too large: %d > %d", n, MaxFrameSize)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("wire: read payload: %w", err)
	}
	var f Frame
	if err := json.Unmarshal(buf, &f); err != nil {
		return nil, fmt.Errorf("wire: unmarshal: %w", err)
	}
	return &f, nil
}
