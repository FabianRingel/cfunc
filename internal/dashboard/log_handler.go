// Package dashboard implements the cfunc operator dashboard: an HTTP UI
// embedded in the gateway binary, plus a slog handler that captures log
// records for live streaming.
package dashboard

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// LogEvent is a captured slog record, JSON-friendly.
type LogEvent struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// captureCore is the shared ring-buffer / pub-sub state that all handler
// facets returned by WithAttrs/WithGroup share. Wrapping at the *core
// boundary keeps mutation thread-safe across slog handler chains.
type captureCore struct {
	cap  int
	mu   sync.Mutex
	ring []LogEvent
	subs map[chan LogEvent]struct{}
}

func (c *captureCore) push(ev LogEvent) {
	c.mu.Lock()
	if len(c.ring) < c.cap {
		c.ring = append(c.ring, ev)
	} else {
		copy(c.ring, c.ring[1:])
		c.ring[len(c.ring)-1] = ev
	}
	for ch := range c.subs {
		select {
		case ch <- ev:
		default: // slow subscriber; drop rather than block log path
		}
	}
	c.mu.Unlock()
}

// LogCapture is a slog.Handler that forwards every record to a downstream
// handler (preserving stdout logging) and additionally feeds a ring
// buffer + live SSE subscribers.
type LogCapture struct {
	downstream slog.Handler
	core       *captureCore
}

// NewLogCapture wraps downstream with a ring buffer of `capacity` events.
func NewLogCapture(downstream slog.Handler, capacity int) *LogCapture {
	if capacity <= 0 {
		capacity = 500
	}
	return &LogCapture{
		downstream: downstream,
		core: &captureCore{
			cap:  capacity,
			ring: make([]LogEvent, 0, capacity),
			subs: map[chan LogEvent]struct{}{},
		},
	}
}

func (l *LogCapture) Enabled(ctx context.Context, lvl slog.Level) bool {
	return l.downstream.Enabled(ctx, lvl)
}

func (l *LogCapture) Handle(ctx context.Context, r slog.Record) error {
	ev := LogEvent{Time: r.Time, Level: r.Level.String(), Message: r.Message}
	if r.NumAttrs() > 0 {
		ev.Attrs = make(map[string]any, r.NumAttrs())
		r.Attrs(func(a slog.Attr) bool {
			ev.Attrs[a.Key] = a.Value.Any()
			return true
		})
	}
	l.core.push(ev)
	return l.downstream.Handle(ctx, r)
}

func (l *LogCapture) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogCapture{downstream: l.downstream.WithAttrs(attrs), core: l.core}
}
func (l *LogCapture) WithGroup(name string) slog.Handler {
	return &LogCapture{downstream: l.downstream.WithGroup(name), core: l.core}
}

// Snapshot returns a copy of the current ring (oldest first).
func (l *LogCapture) Snapshot() []LogEvent {
	l.core.mu.Lock()
	defer l.core.mu.Unlock()
	out := make([]LogEvent, len(l.core.ring))
	copy(out, l.core.ring)
	return out
}

// Subscribe returns a channel of live events and an unsubscribe func.
func (l *LogCapture) Subscribe() (<-chan LogEvent, func()) {
	ch := make(chan LogEvent, 64)
	l.core.mu.Lock()
	l.core.subs[ch] = struct{}{}
	l.core.mu.Unlock()
	return ch, func() {
		l.core.mu.Lock()
		delete(l.core.subs, ch)
		l.core.mu.Unlock()
		close(ch)
	}
}
