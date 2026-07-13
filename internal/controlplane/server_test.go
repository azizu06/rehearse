package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/azizu06/rehearse/internal/controlplane"
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
