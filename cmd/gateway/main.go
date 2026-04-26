// Command gateway is the cfunc HTTP frontend.
//
// It runs two listeners:
//
//   - PUBLIC  (-addr,       default :8080)         /fn/<name>
//   - ADMIN   (-admin-addr, default 127.0.0.1:8081) /_/* (dashboard, admin API, ws)
//
// The admin port hosts everything that should not be world-reachable:
// state, logs, runtime function registration. Defaulting to loopback
// keeps it safe out of the box; binding it elsewhere requires a token.
//
// Token sources (first non-empty wins):
//   -admin-token-file <path>
//   $CFUNC_ADMIN_TOKEN
//   -admin-token <literal>      (least preferred — appears in process list)
//
// Functions can be registered at startup via -fn/-binary, and at any
// time later via:
//
//   POST   /_/api/functions       {"name":"X","binary":"/path",...}
//   DELETE /_/api/functions/X
package main

import (
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fabianringel/cfunc/internal/auth"
	"github.com/fabianringel/cfunc/internal/dashboard"
	"github.com/fabianringel/cfunc/internal/gateway"
)

func main() {
	addr := flag.String("addr", ":8080", "public listen address (function endpoints)")
	adminAddr := flag.String("admin-addr", "127.0.0.1:8081", "admin listen address (dashboard + API)")
	dashPrefix := flag.String("dash", "/_/", "dashboard URL prefix on admin port")
	tokenFile := flag.String("admin-token-file", "", "path to file containing admin token")
	tokenLit := flag.String("admin-token", "", "admin token (literal — env/file preferred)")
	allowedOrigins := flag.String("allowed-origins", "",
		"comma-separated extra Origin patterns the dashboard WebSocket "+
			"will accept (default: same-origin only; use \"*\" to allow any)")
	name := flag.String("fn", "", "(optional) initial function name")
	binary := flag.String("binary", "", "(optional) initial function binary")
	flag.Parse()

	textHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	capture := dashboard.NewLogCapture(textHandler, 1000)
	logger := slog.New(capture)
	slog.SetDefault(logger)

	token, err := auth.LoadToken(*tokenFile, "CFUNC_ADMIN_TOKEN", *tokenLit)
	if err != nil {
		slog.Error("load token", "err", err)
		os.Exit(1)
	}
	if !isLoopback(*adminAddr) && token == "" {
		slog.Error("admin port is non-loopback but no token configured — refusing to start",
			"admin_addr", *adminAddr,
			"hint", "set -admin-token-file, CFUNC_ADMIN_TOKEN, or rebind to 127.0.0.1")
		os.Exit(2)
	}

	gw := gateway.NewWithOptions(gateway.Options{Logger: logger})
	if *binary != "" {
		n := *name
		if n == "" {
			n = "demo"
		}
		gw.Register(n, *binary)
		slog.Info("registered initial function", "name", n, "binary", *binary)
	}

	// Public mux: function endpoints only.
	pubMux := http.NewServeMux()
	pubMux.Handle("/fn/", gw)
	pubMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})

	// Admin mux: dashboard + admin API, gated by token.
	adminMux := http.NewServeMux()
	if *dashPrefix != "" {
		var origins []string
		for _, o := range strings.Split(*allowedOrigins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
		dh := dashboard.NewWithConfig(dashboard.Config{
			Prefix:         *dashPrefix,
			Stats:          statsAdapter{gw},
			Admin:          adminAdapter{gw},
			Logs:           capture,
			AllowedOrigins: origins,
		})
		adminMux.Handle(dh.Prefix(), dh)
	}
	var adminHandler http.Handler = adminMux
	adminHandler = auth.TokenAuth(token, adminHandler)

	go runServer("public", *addr, pubMux)
	go runServer("admin", *adminAddr, adminHandler)

	if *tokenLit != "" {
		slog.Warn("admin token passed via -admin-token flag",
			"hint", "the value is visible in process listings; prefer -admin-token-file or CFUNC_ADMIN_TOKEN")
	}

	authStatus := "open (loopback)"
	if token != "" {
		authStatus = "token-protected"
	}
	slog.Info("gateway up",
		"public", *addr,
		"admin", *adminAddr,
		"auth", authStatus,
		"dashboard", endpointForLog(*dashPrefix))

	// Block forever (servers exit the process on fatal listen errors).
	select {}
}

func runServer(name, addr string, h http.Handler) {
	slog.Info("listening", "name", name, "addr", addr)
	// Bound every phase of an HTTP transaction so a slow client (slowloris,
	// stuck function, half-open connection) cannot pin a goroutine forever.
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("listen", "name", name, "err", err)
		os.Exit(1)
	}
}

// isLoopback returns true if addr binds only to loopback. We accept the
// "127.0.0.1:NNNN" and "localhost:NNNN" forms (and their IPv6 [::1] eq.).
// Anything else — including bare ":NNNN" or "0.0.0.0:NNNN" — is treated
// as exposed and requires a token.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

type statsAdapter struct{ g *gateway.Gateway }

func (s statsAdapter) Stats() any { return s.g.Stats() }

type adminAdapter struct{ g *gateway.Gateway }

func (a adminAdapter) RegisterFunction(req dashboard.RegisterRequest) error {
	def := gateway.FunctionDef{
		Name:           req.Name,
		Binary:         req.Binary,
		Env:            req.Env,
		MaxConcurrency: req.MaxConcurrency,
	}
	for _, l := range req.Layers {
		def.Layers = append(def.Layers, gateway.LayerMount{
			Name: l.Name, HostPath: l.HostPath, MountPath: l.MountPath,
		})
	}
	return a.g.RegisterDef(def)
}

func (a adminAdapter) UnregisterFunction(name string) bool {
	return a.g.Unregister(name)
}

func endpointForLog(p string) string {
	if p == "" {
		return "disabled"
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}
