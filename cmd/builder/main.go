// SPDX-License-Identifier: Apache-2.0

// Command builder is the cfunc layer-build daemon.
//
// It accepts BuildSpec requests over HTTP, runs the build in its
// (controlled) environment, and returns a tarball + manifest. The
// gateway is the only expected client; auth is via shared bearer token.
//
// Deployment: this lives on a dedicated build host. The gateway
// forwards POST /_/api/layers/build to this service via -builder-url.
//
//	cfunc-builder \
//	    -addr=:9090 \
//	    -token-file=/etc/cfunc/builder.token \
//	    -allow-python="3.11,3.12" \
//	    -allow-index="https://pypi.org/simple"
package main

import (
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fabianringel/cfunc/internal/auth"
	"github.com/fabianringel/cfunc/internal/builder"
)

const maxRequestBody = 4 * 1024 * 1024 // requirements.txt is small; cap aggressively

func main() {
	addr := flag.String("addr", "127.0.0.1:9090", "listen address")
	tokenFile := flag.String("token-file", "", "shared secret with the gateway (file)")
	tokenLit := flag.String("token", "", "shared secret (literal — env/file preferred)")
	allowPython := flag.String("allow-python", "",
		"comma-separated allow-list of -python values (empty = any)")
	allowIndex := flag.String("allow-index", "",
		"comma-separated allow-list of HTTPS index_url values (empty = any)")
	timeout := flag.Duration("timeout", 5*time.Minute, "max wall time per build")
	maxOutputMB := flag.Int("max-output-mb", 1024, "reject builds that produce more than this")
	tag := flag.String("tag", hostnameOr("cfunc-builder"), "identifier embedded in produced manifests")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	token, err := auth.LoadToken(*tokenFile, "CFUNC_BUILDER_TOKEN", *tokenLit)
	if err != nil {
		slog.Error("load token", "err", err)
		os.Exit(1)
	}
	if token == "" {
		slog.Error("builder requires a shared token (no anonymous mode)",
			"hint", "set -token-file, -token, or CFUNC_BUILDER_TOKEN")
		os.Exit(2)
	}

	policy := builder.DefaultPolicy()
	if *allowPython != "" {
		policy.AllowedPythonVersions = splitCSV(*allowPython)
	}
	if *allowIndex != "" {
		policy.AllowedIndexURLs = splitCSV(*allowIndex)
	}

	b := builder.New(policy, builder.Limits{
		Timeout:     *timeout,
		MaxOutputMB: *maxOutputMB,
	})
	b.Tag = *tag

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/build", buildHandler(b))

	handler := auth.TokenAuth(token, mux)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      *timeout + 30*time.Second, // respond after the build
		IdleTimeout:       120 * time.Second,
	}
	slog.Info("builder up",
		"addr", *addr,
		"allow_python", policy.AllowedPythonVersions,
		"allow_index", policy.AllowedIndexURLs,
		"timeout", *timeout,
		"max_output_mb", *maxOutputMB)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
}

func buildHandler(b *builder.Builder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

		var spec builder.BuildSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		t0 := time.Now()
		res, err := b.Build(r.Context(), spec)
		if err != nil {
			slog.Warn("build failed",
				"name", spec.Name, "version", spec.Version, "err", err.Error(),
				"duration_ms", time.Since(t0).Milliseconds())
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		slog.Info("build ok",
			"name", spec.Name, "version", spec.Version,
			"digest", res.Manifest.Digest, "size_bytes", res.Manifest.SizeBytes,
			"duration_ms", time.Since(t0).Milliseconds())

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func hostnameOr(def string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return def
}
