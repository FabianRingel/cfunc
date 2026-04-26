package dashboard

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
)

// MaxConcurrencyCeiling caps user-supplied MaxConcurrency to prevent a
// register call from spawning thousands of instances. Operators who
// genuinely need higher numbers can patch this and rebuild.
const MaxConcurrencyCeiling = 256

// MaxAdminBody caps the size of a single admin request. The largest
// realistic register payload is a few KiB; 256 KiB leaves comfortable
// headroom while preventing 1 GB JSON-bombs from authenticated callers.
const MaxAdminBody = 256 * 1024

// serveFunctions handles the collection endpoint:
//   POST /_/api/functions   register or replace
func (h *Handler) serveFunctions(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin API disabled", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxAdminBody)
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// MaxBytesReader exhaustion surfaces as decode error; both map
		// to BadRequest from the caller's perspective.
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Binary = strings.TrimSpace(req.Binary)
	if req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if req.Binary == "" {
		http.Error(w, "binary required", http.StatusBadRequest)
		return
	}
	// Reject relative paths and path-traversal artefacts: the gateway
	// resolves binary against its own cwd, which is not what callers
	// expect. Forcing absolute paths prevents accidental references to
	// repo files and arbitrary-binary mishaps.
	if !filepath.IsAbs(req.Binary) {
		http.Error(w, "binary must be an absolute path", http.StatusBadRequest)
		return
	}
	if filepath.Clean(req.Binary) != req.Binary {
		http.Error(w, "binary must be a clean path (no .. or //)", http.StatusBadRequest)
		return
	}
	if req.MaxConcurrency < 0 {
		http.Error(w, "max_concurrency must be >= 0", http.StatusBadRequest)
		return
	}
	if req.MaxConcurrency > MaxConcurrencyCeiling {
		http.Error(w, "max_concurrency exceeds ceiling", http.StatusBadRequest)
		return
	}
	if err := h.admin.RegisterFunction(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"name":     req.Name,
		"endpoint": "/fn/" + req.Name,
	})
}

// serveFunction handles the item endpoint:
//   DELETE /_/api/functions/<name>   unregister
func (h *Handler) serveFunction(w http.ResponseWriter, r *http.Request, name string) {
	if h.admin == nil {
		http.Error(w, "admin API disabled", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if !h.admin.UnregisterFunction(name) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
