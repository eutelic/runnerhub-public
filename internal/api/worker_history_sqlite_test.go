//go:build !localtest

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestHistoryEndpointUsesSQLitePagingAndViewerAuthorization(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, w := range []core.Worker{
		{ID: "w-a", Repository: "eutelic/runnerhub", State: core.WorkerDeleted,
			JobResult: "failure", FailureReason: "disk full", UpdatedAt: time.Now().UTC()},
		{ID: "w-b", Repository: "other/repo", State: core.WorkerCompleted,
			JobResult: "success", UpdatedAt: time.Now().UTC()},
	} {
		if err := db.UpsertWorker(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	mgr := auth.New(auth.Config{APIKeys: map[string]auth.Principal{
		"viewer-token": {Name: "viewer", Role: auth.RoleViewer},
		"scaler-token": {Name: "scaler", Role: auth.RoleScaler},
	}})
	handler := (&Service{Store: db, Auth: mgr}).Handler()
	call := func(token, url string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	ok := call("viewer-token", "/api/v1/workers/history?repository=EUTELIC/runnerhub&state=failed&limit=1")
	if ok.Code != http.StatusOK {
		t.Fatalf("viewer status %d body: %s", ok.Code, ok.Body.String())
	}
	var page workerHistoryPage
	if err := json.Unmarshal(ok.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "w-a" || page.HasMore {
		t.Fatalf("unexpected history page: %+v", page)
	}
	if response := call("viewer-token", "/api/v1/workers/history?limit=101"); response.Code != 400 {
		t.Fatalf("invalid page limit status %d", response.Code)
	}
	if response := call("scaler-token", "/api/v1/workers/history"); response.Code != 403 {
		t.Fatalf("scaler must not access history: %d", response.Code)
	}
	if response := call("incorrect", "/api/v1/workers/history"); response.Code != 401 {
		t.Fatalf("unauthenticated history status %d", response.Code)
	}
}

func TestHistoryEndpointRejectsInvalidDateBounds(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := auth.New(auth.Config{APIKeys: map[string]auth.Principal{
		"viewer-token": {Name: "viewer", Role: auth.RoleViewer},
	}})
	handler := (&Service{Store: db, Auth: mgr}).Handler()
	for _, u := range []string{
		"/api/v1/workers/history?from=2026-02-30",
		"/api/v1/workers/history?to=2026-11-99",
		"/api/v1/workers/history?from=2026-11-03&to=2026-11-02",
		"/api/v1/workers/history?from=2026-11-02T12:00:00Z",
	} {
		req := httptest.NewRequest(http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Bearer viewer-token")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", u, rr.Code, rr.Body.String())
		}
	}
}
