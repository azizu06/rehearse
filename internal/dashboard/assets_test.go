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

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); !strings.Contains(body, `<div id="root"></div>`) {
		t.Fatalf("body does not contain dashboard root: %q", body)
	}
}
