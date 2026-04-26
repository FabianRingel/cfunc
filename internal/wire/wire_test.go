// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRoundTripInvoke(t *testing.T) {
	event, _ := json.Marshal(map[string]string{"path": "/hello"})
	in := &Frame{Type: TypeInvoke, ID: "req-1", Event: event}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out.Type != TypeInvoke || out.ID != "req-1" {
		t.Fatalf("got %+v", out)
	}
	if string(out.Event) != string(event) {
		t.Fatalf("event mismatch: %s vs %s", out.Event, event)
	}
}

func TestRoundTripError(t *testing.T) {
	in := &Frame{Type: TypeError, ID: "x", Error: &ErrorPayload{Type: "Boom", Message: "nope"}}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if out.Error == nil || out.Error.Message != "nope" {
		t.Fatalf("got %+v", out.Error)
	}
}

func TestMultipleFramesSequential(t *testing.T) {
	var buf bytes.Buffer
	for i, id := range []string{"a", "b", "c"} {
		_ = i
		if err := WriteFrame(&buf, &Frame{Type: TypeInvoke, ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "b", "c"} {
		f, err := ReadFrame(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if f.ID != want {
			t.Fatalf("got %s want %s", f.ID, want)
		}
	}
	if _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestEOFOnEmpty(t *testing.T) {
	if _, err := ReadFrame(&bytes.Buffer{}); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestRejectsOversizedHeader(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxFrameSize+1)
	r := bytes.NewReader(hdr[:])
	if _, err := ReadFrame(r); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too-large error, got %v", err)
	}
}

func TestRejectsZeroLength(t *testing.T) {
	var hdr [4]byte
	r := bytes.NewReader(hdr[:])
	if _, err := ReadFrame(r); err == nil {
		t.Fatal("expected error for zero-length frame")
	}
}

func TestRejectsBadJSON(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("not json")
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	buf.Write(hdr[:])
	buf.Write(payload)
	if _, err := ReadFrame(&buf); err == nil {
		t.Fatal("expected unmarshal error")
	}
}
