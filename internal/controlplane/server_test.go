package controlplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/controlplane"
	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/evidence"
	"github.com/azizu06/rehearse/internal/probe"
	"github.com/azizu06/rehearse/internal/redact"
)

func TestVersionedAPI(t *testing.T) {
	t.Parallel()

	handler := controlplane.NewHandler(controlplane.Options{Version: "test-version"})
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   map[string]string
	}{
		{
			name:       "health",
			path:       "/api/v1/health",
			wantStatus: http.StatusOK,
			wantBody:   map[string]string{"status": "ok"},
		},
		{
			name:       "version",
			path:       "/api/v1/version",
			wantStatus: http.StatusOK,
			wantBody:   map[string]string{"version": "test-version"},
		},
		{
			name:       "unknown versioned route",
			path:       "/api/v1/missing",
			wantStatus: http.StatusNotFound,
			wantBody:   map[string]string{"error": "not found"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}

			var got map[string]string
			if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if gotKey, wantKey := onlyEntry(got), onlyEntry(test.wantBody); gotKey != wantKey {
				t.Fatalf("body = %q, want %q", gotKey, wantKey)
			}
		})
	}
}

func TestReportAPIReappliesRedaction(t *testing.T) {
	t.Parallel()

	secret := "issue-10-api-secret"
	startedAt := time.Date(2026, time.July, 15, 21, 0, 0, 0, time.UTC)
	report := evidence.Report{
		SchemaVersion:    evidence.SchemaVersion,
		SnapshotSequence: 1,
		SnapshotAt:       startedAt.Add(time.Second),
		RunID:            "run-10", PlanID: "plan-10", PlanVersion: 1,
		RecoveryPoint: evidence.RecoveryPoint{ID: "snapshot-" + secret, SelectedAt: startedAt},
		Stages:        []evidence.Stage{{Ordinal: 1, Name: drill.StageProbe, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Second), Duration: time.Second}},
		Probes:        []probe.Evidence{{Ordinal: 1, ID: "health", Kind: probe.KindHTTP, Required: true, Status: probe.StatusPassed, Attempts: 1, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Second), Duration: time.Second, Observed: secret}},
		Outcome:       drill.OutcomeSucceeded, Cleanup: drill.CleanupSucceeded,
	}
	handler := controlplane.NewHandler(controlplane.Options{
		ReportReader: staticReportReader{view: evidence.ReportView{
			Snapshot: report,
			CurrentCleanup: evidence.CurrentCleanup{
				Status:       drill.CleanupSucceeded,
				AsOfSequence: report.SnapshotSequence,
				AsOf:         report.SnapshotAt,
			},
		}},
		Redactor: redact.New(secret),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-10/report", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte(secret)) || !bytes.Contains(response.Body.Bytes(), []byte(redact.Replacement)) {
		t.Fatalf("API report redaction = %s", response.Body.String())
	}
}

type staticReportReader struct {
	view evidence.ReportView
	err  error
}

func (reader staticReportReader) ReportView(context.Context, string) (evidence.ReportView, error) {
	return reader.view, reader.err
}

func TestDashboardIsServedOutsideTheAPINamespace(t *testing.T) {
	t.Parallel()

	dashboard := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTeapot)
	})
	handler := controlplane.NewHandler(controlplane.Options{Dashboard: dashboard})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusTeapot)
	}
}

func onlyEntry(values map[string]string) string {
	for key, value := range values {
		return key + "=" + value
	}
	return ""
}
