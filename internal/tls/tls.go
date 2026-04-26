// SPDX-License-Identifier: Apache-2.0

// Package tls integrates ACME-issued TLS certs into cfunc via certmagic.
//
// Two modes:
//
//   HTTP-01 (default):  certmagic listens on :80 for the ACME challenge,
//                       then certs are served on the gateway's port via
//                       the returned *tls.Config. Single-host only — no
//                       wildcards.
//
//   DNS-01:             ACME challenge resolved via a libdns provider
//                       (Cloudflare/Hetzner/Route53/DigitalOcean/
//                       RFC2136). Required for wildcard certs and for
//                       gateways behind firewalls.
//
// Provider registration is in internal/tls/providers/<name>.go; importing
// the package (with `_ "..."`) registers it.
package tls

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"sync"

	"github.com/caddyserver/certmagic"
)

// DNSProvider is what an ACME DNS-01 challenge needs from a libdns
// provider. Re-exported so providers don't import certmagic directly.
type DNSProvider = certmagic.DNSProvider

// Env wraps environment-variable lookup with helpers so providers can
// validate their config uniformly.
type Env struct{}

// Required returns the value of name, or an error if it's empty.
func (Env) Required(name string) (string, error) {
	if v := os.Getenv(name); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("tls: required env var %s is not set", name)
}

// Optional returns the value of name or the default if unset.
func (Env) Optional(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// Factory builds a DNSProvider from environment configuration.
type Factory func(env Env) (DNSProvider, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register adds a provider to the global registry. Called from a
// provider package's init().
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = f
}

// Providers returns registered provider names, sorted.
func Providers() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Config controls a TLS-managed listener.
type Config struct {
	Domains  []string     // wildcards only valid with DNS-01
	Email    string       // ACME account email (required by LE)
	Storage  string       // cert cache directory (default: certmagic standard)
	Provider string       // DNS-01 provider name; empty = HTTP-01
	Staging  bool         // route to LE staging
	Logger   *slog.Logger // nil = slog.Default()
}

// Manager is the handle returned by Setup.
type Manager struct {
	cfg       *certmagic.Config
	issuer    *certmagic.ACMEIssuer
	needsHTTP bool
}

// TLSConfig returns a *tls.Config to plug into an http.Server.
func (m *Manager) TLSConfig() *tls.Config { return m.cfg.TLSConfig() }

// HTTPChallengeHandler wraps next with certmagic's ACME HTTP-01 hook.
// Mount this on a port-80 server when NeedsHTTPChallenge() is true.
// When DNS-01 is configured, returns next unchanged.
func (m *Manager) HTTPChallengeHandler(next http.Handler) http.Handler {
	if !m.needsHTTP {
		return next
	}
	return m.issuer.HTTPChallengeHandler(next)
}

// NeedsHTTPChallenge reports whether a port-80 listener is required.
func (m *Manager) NeedsHTTPChallenge() bool { return m.needsHTTP }

// Setup wires certmagic with the requested configuration and
// synchronously acquires certs for all Domains.
func Setup(ctx context.Context, c Config) (*Manager, error) {
	if len(c.Domains) == 0 {
		return nil, errors.New("tls: at least one domain required")
	}
	if c.Email == "" {
		return nil, errors.New("tls: email required for ACME account")
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}

	if c.Storage != "" {
		certmagic.Default.Storage = &certmagic.FileStorage{Path: c.Storage}
	}

	cfg := certmagic.NewDefault()
	ca := certmagic.LetsEncryptProductionCA
	if c.Staging {
		ca = certmagic.LetsEncryptStagingCA
	}
	issuer := certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{
		Email:  c.Email,
		Agreed: true,
		CA:     ca,
	})

	if c.Provider != "" {
		registryMu.RLock()
		factory, ok := registry[c.Provider]
		registryMu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("tls: unknown DNS provider %q (registered: %v)",
				c.Provider, Providers())
		}
		dns, err := factory(Env{})
		if err != nil {
			return nil, fmt.Errorf("tls: provider %s: %w", c.Provider, err)
		}
		issuer.DNS01Solver = &certmagic.DNS01Solver{
			DNSManager: certmagic.DNSManager{DNSProvider: dns},
		}
		c.Logger.Info("tls: DNS-01 challenge enabled", "provider", c.Provider)
	} else {
		c.Logger.Info("tls: HTTP-01 challenge (port 80 must be reachable)")
	}

	cfg.Issuers = []certmagic.Issuer{issuer}

	c.Logger.Info("tls: acquiring certificates",
		"domains", c.Domains, "staging", c.Staging)
	if err := cfg.ManageSync(ctx, c.Domains); err != nil {
		return nil, fmt.Errorf("tls: ManageSync: %w", err)
	}
	c.Logger.Info("tls: certificates ready", "domains", c.Domains)

	return &Manager{cfg: cfg, issuer: issuer, needsHTTP: c.Provider == ""}, nil
}
