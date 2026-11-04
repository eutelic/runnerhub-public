package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueuedGitHubJobsRouteRequiresGitHubAuth(t *testing.T) {
	service := &Service{}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/github/repositories/eutelic/private/queued-jobs", nil)
	response := httptest.NewRecorder()

	service.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
}
