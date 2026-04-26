// Package auth implements lightweight token authentication for the
// cfunc admin/dashboard surface.
//
// The middleware accepts the token in two places:
//
//   - Authorization: Bearer <token>      — for API calls (curl, fetch)
//   - ?token=<token>                     — for browser WebSockets
//                                          (the WebSocket constructor
//                                          can't set custom headers)
//
// Comparison is constant-time. An empty configured token disables the
// check entirely — the gateway uses that mode when binding to loopback,
// where transport-layer isolation already provides protection.
package auth

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
)

// TokenAuth wraps next with a bearer-token check. If token is empty the
// middleware is a passthrough (no auth required).
func TokenAuth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	wantBytes := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validToken(r, wantBytes) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="cfunc"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validToken extracts the token from either header or query and
// constant-time compares it to want.
func validToken(r *http.Request, want []byte) bool {
	if got := bearer(r); got != "" {
		return subtle.ConstantTimeCompare([]byte(got), want) == 1
	}
	if got := r.URL.Query().Get("token"); got != "" {
		return subtle.ConstantTimeCompare([]byte(got), want) == 1
	}
	return false
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// LoadToken returns the first non-empty value from:
//
//	1. tokenFile contents (if non-empty path),
//	2. envVar value,
//	3. literal,
//
// trimmed of trailing whitespace. Empty result means "no token
// configured" — caller is responsible for deciding whether that is OK
// for its bind address.
func LoadToken(tokenFile, envVar, literal string) (string, error) {
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", err
		}
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	if envVar != "" {
		if t := strings.TrimSpace(os.Getenv(envVar)); t != "" {
			return t, nil
		}
	}
	return strings.TrimSpace(literal), nil
}
