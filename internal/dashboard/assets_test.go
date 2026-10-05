package dashboard_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azizu06/rehearse/internal/dashboard"
)

func TestHandlerServesEmbeddedDashboard(t *testing.T) {
	t.Parallel()

	handler, err := dashboard.Handler()
	if err != nil {
		t.Fatalf("create dashboard handler: %v", err)
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "root entrypoint",
			path:       "/",
			wantStatus: http.StatusOK,
			wantBody:   `<div id="root"></div>`,
		},
		{
			name:       "embedded static asset",
			path:       "/assets/index.css",
			wantStatus: http.StatusOK,
			wantBody:   ":root{color:#17242b",
		},
		{
			name:       "path not in the embedded bundle",
			path:       "/assets/missing.js",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			if tt.wantBody != "" {
				if body := response.Body.String(); !strings.Contains(body, tt.wantBody) {
					t.Fatalf("body does not contain %q: %q", tt.wantBody, body)
				}
			}
		})
	}
}
