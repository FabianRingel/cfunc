// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"

	"github.com/fabianringel/cfunc/internal/state"
)

// Identity is the resolved caller of an authenticated request. Routes
// can extract it from the request context with FromContext.
//
// A cluster-admin identity (the legacy single-token admin path) has
// KeyID="admin-token", Project="*", Scopes={"admin"}. Any project-scoped
// route must reject Project=="*" or treat it as "any" depending on
// semantics.
type Identity struct {
	KeyID   string
	Project string
	Scopes  []string
}

// HasScope returns true if scope appears in i.Scopes or i is cluster-admin.
func (i Identity) HasScope(scope string) bool {
	if i.KeyID == "admin-token" {
		return true
	}
	for _, s := range i.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

type identityCtxKey struct{}

// WithIdentity returns ctx carrying id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// FromContext returns the Identity stored on ctx, or the zero value.
// Callers that require auth should already have run middleware that
// rejects unauthenticated requests, so a zero KeyID is a bug.
func FromContext(ctx context.Context) Identity {
	id, _ := ctx.Value(identityCtxKey{}).(Identity)
	return id
}

// APIKeyAuth wraps next with a two-tier check:
//
//  1. If adminToken is non-empty and the bearer matches, the request
//     proceeds as cluster admin. This preserves the 0.2 behaviour for
//     operators who haven't migrated to per-project keys.
//  2. Otherwise, the bearer is sha256-hashed and looked up in store.
//     A match installs the API-key identity and proceeds.
//  3. requireScope, if non-empty, is enforced after identity resolution.
//
// store may be nil — in that case only the admin-token path is active.
// Empty adminToken AND nil store means "no auth" (passthrough), matching
// the legacy TokenAuth("", h) behaviour.
func APIKeyAuth(adminToken string, store state.Store, requireScope string, next http.Handler) http.Handler {
	if adminToken == "" && store == nil {
		return next
	}
	adminBytes := []byte(adminToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := bearerOrQuery(r)
		if got == "" {
			unauth(w)
			return
		}

		var id Identity
		if adminToken != "" && subtle.ConstantTimeCompare([]byte(got), adminBytes) == 1 {
			id = Identity{KeyID: "admin-token", Project: "*", Scopes: []string{"admin"}}
		} else if store != nil {
			h := sha256.Sum256([]byte(got))
			k, err := store.LookupAPIKey(r.Context(), h[:])
			if err != nil {
				unauth(w)
				return
			}
			id = Identity{KeyID: k.ID, Project: k.Project, Scopes: k.Scopes}
		} else {
			unauth(w)
			return
		}

		if requireScope != "" && !id.HasScope(requireScope) {
			http.Error(w, "forbidden: missing scope "+requireScope, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

func bearerOrQuery(r *http.Request) string {
	if t := bearer(r); t != "" {
		return t
	}
	return r.URL.Query().Get("token")
}

func unauth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="cfunc"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

