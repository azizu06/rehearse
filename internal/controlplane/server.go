// Package controlplane owns the local HTTP API and dashboard boundary for the
// Rehearse process.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/azizu06/rehearse/internal/evidence"
	"github.com/azizu06/rehearse/internal/redact"
)

type ReportReader interface {
	Report(context.Context, string) (evidence.Report, error)
}

// Options contains the dependencies exposed by the local control plane.
type Options struct {
	Version      string
	Dashboard    http.Handler
	ReportReader ReportReader
	Redactor     redact.Redactor
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
	mux.HandleFunc("GET /api/v1/runs/{runID}/report", func(response http.ResponseWriter, request *http.Request) {
		runID := request.PathValue("runID")
		if !runIDPattern.MatchString(runID) || options.ReportReader == nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		report, err := options.ReportReader.Report(request.Context(), runID)
		if errors.Is(err, evidence.ErrReportNotFound) {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "report unavailable"})
			return
		}
		encoded, err := report.CanonicalJSON(options.Redactor)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "report unavailable"})
			return
		}
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write(encoded)
	})
	mux.HandleFunc("GET /api/v1/", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	mux.Handle("GET /", options.Dashboard)

	return mux
}

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
