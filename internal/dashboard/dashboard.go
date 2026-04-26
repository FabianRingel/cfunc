package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:web/dist
var distFS embed.FS

// StatsProvider is whatever can hand the dashboard a current snapshot.
// The gateway implements this; tests can use a stub.
type StatsProvider interface {
	Stats() any
}

// Admin is the surface the runtime-registration API uses. Optional —
// New() works with stats-only; admin endpoints 404 without it.
type Admin interface {
	RegisterFunction(req RegisterRequest) error
	UnregisterFunction(name string) bool
}

// RegisterRequest is the JSON body accepted by POST /_/api/functions.
type RegisterRequest struct {
	Name           string             `json:"name"`
	Binary         string             `json:"binary"`
	Env            []string           `json:"env,omitempty"`
	Layers         []RegisterLayerRef `json:"layers,omitempty"`
	MaxConcurrency int                `json:"max_concurrency,omitempty"`
}

type RegisterLayerRef struct {
	Name      string `json:"name"`
	HostPath  string `json:"host_path"`
	MountPath string `json:"mount_path"`
}

// Handler is an http.Handler mounted under a fixed path prefix
// (default /_/) that serves the React dashboard bundle plus its
// WebSocket and JSON APIs.
type Handler struct {
	prefix         string
	stats          StatsProvider
	admin          Admin // optional; nil = admin endpoints disabled
	logs           *LogCapture
	allowedOrigins []string // nil/empty -> same-origin only

	files     fs.FS
	staticSrv http.Handler
}

// Config bundles dashboard options.
type Config struct {
	Prefix string // URL prefix, default "/_/"
	Stats  StatsProvider
	Admin  Admin // optional
	Logs   *LogCapture
	// AllowedOrigins, if non-empty, lists patterns the WebSocket Accept
	// will compare the Origin header against (see coder/websocket
	// AcceptOptions.OriginPatterns). Empty/nil means same-origin only.
	// Use []string{"*"} to opt-in to allowing every origin (insecure).
	AllowedOrigins []string
}

// New wires the dashboard with default config (no admin API).
func New(prefix string, stats StatsProvider, logs *LogCapture) *Handler {
	return NewWithConfig(Config{Prefix: prefix, Stats: stats, Logs: logs})
}

// NewWithAdmin keeps backwards compatibility.
func NewWithAdmin(prefix string, stats StatsProvider, admin Admin, logs *LogCapture) *Handler {
	return NewWithConfig(Config{Prefix: prefix, Stats: stats, Admin: admin, Logs: logs})
}

// NewWithConfig is the full-control constructor.
func NewWithConfig(cfg Config) *Handler {
	prefix := cfg.Prefix
	if prefix == "" || !strings.HasSuffix(prefix, "/") {
		prefix = "/_/"
	}
	sub, err := fs.Sub(distFS, "web/dist")
	if err != nil {
		sub = distFS
	}
	return &Handler{
		prefix:         prefix,
		stats:          cfg.Stats,
		admin:          cfg.Admin,
		logs:           cfg.Logs,
		allowedOrigins: cfg.AllowedOrigins,
		files:          sub,
		staticSrv:      http.StripPrefix(prefix, http.FileServer(http.FS(sub))),
	}
}

// Prefix returns the URL prefix this handler serves.
func (h *Handler) Prefix() string { return h.prefix }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, h.prefix)
	switch {
	case rest == "" || rest == "index.html":
		h.serveIndex(w, r)
	case rest == "ws":
		h.serveWS(w, r)
	case rest == "api/state":
		serveJSON(w, h.stats.Stats())
	case rest == "api/functions":
		h.serveFunctions(w, r)
	case strings.HasPrefix(rest, "api/functions/"):
		h.serveFunction(w, r, strings.TrimPrefix(rest, "api/functions/"))
	default:
		// Asset (e.g. /_/assets/index-XYZ.js). FileServer handles 404.
		h.staticSrv.ServeHTTP(w, r)
	}
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(h.files, "index.html")
	if err != nil {
		http.Error(w, "dashboard bundle missing — run `make dashboard`", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func serveJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := newEncoder(w)
	_ = enc.Encode(v)
}
