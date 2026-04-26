// SPDX-License-Identifier: Apache-2.0

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
// TLS:
//   -tls-domain "fn.example.org,*.fn.example.org"  ACME-managed certs
//   -tls-email ops@example.org
//   -tls-dns-provider hetzner                       DNS-01 (any libdns provider
//                                                    registered in internal/tls)
//   -tls-staging                                    use Let's Encrypt staging
//   -admin-tls-domain admin.example.org             separate cert for admin
//
// Functions can be registered at startup via -fn/-binary, and at any
// time later via:
//
//   POST   /_/api/functions       {"name":"X","binary":"/path",...}
//   DELETE /_/api/functions/X
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fabianringel/cfunc/internal/auth"
	"github.com/fabianringel/cfunc/internal/dashboard"
	"github.com/fabianringel/cfunc/internal/gateway"
	"github.com/fabianringel/cfunc/internal/state"
	cftls "github.com/fabianringel/cfunc/internal/tls"

	// Side-effect imports register libdns providers in the global
	// registry; pick the ones you want compiled in.
	_ "github.com/fabianringel/cfunc/internal/tls/providers"
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

	tlsDomain := flag.String("tls-domain", "",
		"comma-separated domains for ACME on the public port; empty disables TLS")
	tlsEmail := flag.String("tls-email", "", "ACME contact email (required if -tls-domain set)")
	tlsStorage := flag.String("tls-storage", "",
		"directory to cache certs (default: certmagic standard path)")
	tlsDNS := flag.String("tls-dns-provider", "",
		"libdns provider for DNS-01 challenge; empty = HTTP-01")
	tlsStaging := flag.Bool("tls-staging", false, "use Let's Encrypt staging")
	tlsHTTPAddr := flag.String("tls-http-addr", ":80",
		"port-80 listener for HTTP-01 challenges + HTTP→HTTPS redirect")

	adminTLSDomain := flag.String("admin-tls-domain", "",
		"comma-separated domains for ACME on the admin port; empty = HTTP")

	stateDSN := flag.String("state-dsn", "",
		"Postgres DSN for cluster-coordinated state (functions, crons). "+
			"Empty = single-node in-memory store.")

	builderURL := flag.String("builder-url", "",
		"base URL of cfunc-builder (e.g. http://10.0.0.5:9090). "+
			"When set, /_/api/layers/build is forwarded to the builder.")
	builderTokenFile := flag.String("builder-token-file", "",
		"file with the bearer token shared with the builder")
	builderToken := flag.String("builder-token", "",
		"literal bearer token for the builder (env/file preferred)")

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

	var stateStore state.Store
	if *stateDSN != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		ps, err := state.OpenPostgres(ctx, *stateDSN)
		cancel()
		if err != nil {
			slog.Error("state: open postgres", "err", err)
			os.Exit(1)
		}
		stateStore = ps
		slog.Info("state: postgres mode", "dsn_host", redactDSN(*stateDSN))
	}

	gw := gateway.NewWithOptions(gateway.Options{Logger: logger, Store: stateStore})
	if *binary != "" {
		n := *name
		if n == "" {
			n = "demo"
		}
		gw.Register(n, *binary)
		slog.Info("registered initial function", "name", n, "binary", *binary)
	}

	pubMux := http.NewServeMux()
	pubMux.Handle("/fn/", gw)
	pubMux.Handle("/v1/", gw) // multi-tenant routing: /v1/<project>/fn/<name>
	pubMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})

	adminMux := http.NewServeMux()
	if *dashPrefix != "" {
		var origins []string
		for _, o := range strings.Split(*allowedOrigins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
		var bldr dashboard.LayerBuilder
		if *builderURL != "" {
			bToken, err := auth.LoadToken(*builderTokenFile, "CFUNC_BUILDER_TOKEN", *builderToken)
			if err != nil {
				slog.Error("builder token load", "err", err)
				os.Exit(1)
			}
			if bToken == "" {
				slog.Error("-builder-url set but no builder token configured")
				os.Exit(2)
			}
			bldr = &builderClient{baseURL: strings.TrimRight(*builderURL, "/"), token: bToken}
		}
		dh := dashboard.NewWithConfig(dashboard.Config{
			Prefix:         *dashPrefix,
			Stats:          statsAdapter{gw},
			Admin:          adminAdapter{gw},
			Builder:        bldr,
			Logs:           capture,
			AllowedOrigins: origins,
		})
		adminMux.Handle(dh.Prefix(), dh)
	}
	// Auth-gating: the dashboard's static bundle (HTML/JS/CSS) must
	// remain reachable without a token, otherwise the operator can
	// never see the login screen to enter the token in. Only the API
	// paths (`/_/api/*`) and the WebSocket (`/_/ws`) require auth.
	// Anything outside the dashboard prefix (which today is nothing,
	// but defensively) falls back to full token-gating.
	adminHandler := dashboardAuthSplit(*dashPrefix, token, adminMux)

	ctx := context.Background()

	// Acquire certs (synchronous; blocks until LE responds). Done before
	// listeners come up so a misconfig fails fast at startup.
	pubTLS, err := setupTLS(ctx, *tlsDomain, *tlsEmail, *tlsStorage, *tlsDNS, *tlsStaging, "public", logger)
	if err != nil {
		slog.Error("public TLS setup failed", "err", err)
		os.Exit(1)
	}
	adminTLS, err := setupTLS(ctx, *adminTLSDomain, *tlsEmail, *tlsStorage, *tlsDNS, *tlsStaging, "admin", logger)
	if err != nil {
		slog.Error("admin TLS setup failed", "err", err)
		os.Exit(1)
	}

	go runServer("public", *addr, pubMux, pubTLS)
	go runServer("admin", *adminAddr, adminHandler, adminTLS)

	// HTTP-01 needs a port-80 listener for ACME challenges. We mount it
	// once (covering both public and admin if both opted in) and add a
	// best-effort HTTPS redirect for the public host.
	if (pubTLS != nil && pubTLS.NeedsHTTPChallenge()) ||
		(adminTLS != nil && adminTLS.NeedsHTTPChallenge()) {
		go runChallengeServer(*tlsHTTPAddr, pubTLS, adminTLS, *tlsDomain)
	}

	if *tokenLit != "" {
		slog.Warn("admin token passed via -admin-token flag",
			"hint", "the value is visible in process listings; prefer -admin-token-file or CFUNC_ADMIN_TOKEN")
	}

	slog.Info("gateway up",
		"public", *addr,
		"admin", *adminAddr,
		"auth", authStatus(token),
		"dashboard", endpointForLog(*dashPrefix),
		"tls_public", *tlsDomain != "",
		"tls_admin", *adminTLSDomain != "")

	select {}
}

// setupTLS returns nil if domain is empty (TLS disabled for that port).
func setupTLS(ctx context.Context, domains, email, storage, provider string, staging bool,
	tag string, logger *slog.Logger) (*cftls.Manager, error) {
	if domains == "" {
		return nil, nil
	}
	if email == "" {
		return nil, &configError{tag: tag, msg: "-tls-email required when -tls-domain or -admin-tls-domain is set"}
	}
	var ds []string
	for _, d := range strings.Split(domains, ",") {
		if d = strings.TrimSpace(d); d != "" {
			ds = append(ds, d)
		}
	}
	return cftls.Setup(ctx, cftls.Config{
		Domains:  ds,
		Email:    email,
		Storage:  storage,
		Provider: provider,
		Staging:  staging,
		Logger:   logger.With("tls", tag),
	})
}

func runServer(name, addr string, h http.Handler, tlsMgr *cftls.Manager) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if tlsMgr != nil {
		srv.TLSConfig = tlsMgr.TLSConfig()
		slog.Info("listening (TLS)", "name", name, "addr", addr)
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			slog.Error("listen", "name", name, "err", err)
			os.Exit(1)
		}
		return
	}
	slog.Info("listening", "name", name, "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("listen", "name", name, "err", err)
		os.Exit(1)
	}
}

// runChallengeServer brings up a port-80 listener that:
//   1. answers ACME HTTP-01 challenges via certmagic
//   2. issues 301 redirects from http:// to https:// for the primary
//      public domain
func runChallengeServer(addr string, pubTLS, adminTLS *cftls.Manager, primary string) {
	mux := http.NewServeMux()
	primaryHost := strings.SplitN(strings.TrimSpace(strings.Split(primary, ",")[0]), ":", 2)[0]
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if primaryHost != "" {
			host = primaryHost
		}
		http.Redirect(w, r, "https://"+host+r.RequestURI, http.StatusMovedPermanently)
	})
	mux.Handle("/", redirect)

	// certmagic's challenge handler intercepts /.well-known/acme-challenge/.
	var h http.Handler = mux
	if pubTLS != nil {
		h = pubTLS.HTTPChallengeHandler(h)
	}
	if adminTLS != nil {
		h = adminTLS.HTTPChallengeHandler(h)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("listening (HTTP-01 + redirect)", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("HTTP-01 listener", "err", err)
		os.Exit(1)
	}
}

// dashboardAuthSplit returns an http.Handler that requires the bearer
// token only for the dashboard's API and WebSocket routes — the
// static bundle is served unauthenticated so the operator can reach
// the React login screen and enter the token there. Anything outside
// the dashboard prefix falls back to full token-gating, so accidental
// new routes don't sneak past the auth check.
func dashboardAuthSplit(prefix, token string, next http.Handler) http.Handler {
	authed := auth.TokenAuth(token, next)
	if token == "" {
		return next // no auth configured anywhere
	}
	apiPrefix := prefix + "api/"
	wsPath := prefix + "ws"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, apiPrefix) || p == wsPath || !strings.HasPrefix(p, prefix) {
			authed.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authStatus(token string) string {
	if token == "" {
		return "open (loopback)"
	}
	return "token-protected"
}

// isLoopback returns true if addr binds only to loopback.
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

type configError struct{ tag, msg string }

func (e *configError) Error() string { return e.tag + ": " + e.msg }

// builderClient is a thin HTTP wrapper around the cfunc-builder
// daemon. Exposes dashboard.LayerBuilder; the dashboard relays the
// builder's response to the operator unmodified.
type builderClient struct {
	baseURL string
	token   string
}

func (c *builderClient) BuildLayer(spec []byte) ([]byte, error) {
	req, err := http.NewRequest("POST", c.baseURL+"/build", bytes.NewReader(spec))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	cl := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("builder unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("builder returned %d: %s", resp.StatusCode, body)
	}
	return body, nil
}

// redactDSN strips credentials from a Postgres URL for logging.
func redactDSN(dsn string) string {
	// Quick best-effort: trim everything before "@" if present.
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return dsn
	}
	scheme := strings.Index(dsn, "://")
	if scheme < 0 {
		return dsn
	}
	return dsn[:scheme+3] + "***" + dsn[at:]
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
		Project:        req.Project,
	}
	for _, l := range req.Layers {
		def.Layers = append(def.Layers, gateway.LayerMount{
			Name: l.Name, HostPath: l.HostPath, MountPath: l.MountPath, Digest: l.Digest,
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
