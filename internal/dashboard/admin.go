package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
)

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
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Binary) == "" {
		http.Error(w, "binary required", http.StatusBadRequest)
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
