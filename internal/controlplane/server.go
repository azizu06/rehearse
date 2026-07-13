// Package controlplane owns the local HTTP API and dashboard boundary for the
// Rehearse process.
package controlplane

import (
	"encoding/json"
	"net/http"
)

// Options contains the dependencies exposed by the local control plane.
type Options struct {
	Version   string
	Dashboard http.Handler
}

// NewHandler constructs the versioned API and dashboard handler.
func NewHandler(options Options) http.Handler {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.Dashboard == nil {
		options.Dashboard = http.NotFoundHandler()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/version", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, map[string]string{"version": options.Version})
	})
	mux.HandleFunc("GET /api/v1/", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	mux.Handle("GET /", options.Dashboard)

	return mux
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
