package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestWorkerSnapshotExcludesDeletedOnlyWhenRequested(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, worker := range []core.Worker{
		{ID: "old", State: core.WorkerDeleted, ProviderDiagnostics: "historical private diagnostics"},
		{ID: "active", State: core.WorkerBusy, ProviderDiagnostics: "current private diagnostics"},
	} {
		if err := db.UpsertWorker(ctx, worker); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{Store: db, Auth: auth.New(auth.Config{
		APIKeys: auth.ParseKeys("scale:scaler:listener,admin:admin:local"),
	})}
	check := func(token, path string, want int, wantActiveDiagnostics bool) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		svc.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, res.Code, res.Body.String())
		}
		var workers []core.Worker
		if err := json.Unmarshal(res.Body.Bytes(), &workers); err != nil {
			t.Fatal(err)
		}
		if len(workers) != want {
			t.Fatalf("GET %s workers=%v want %d", path, workers, want)
		}
		if want == 1 {
			if workers[0].ID != "active" {
				t.Fatalf("GET %s returned wrong worker %s", path, workers[0].ID)
			}
			hasDetails := workers[0].ProviderDiagnostics != ""
			if hasDetails != wantActiveDiagnostics {
				t.Fatalf("GET %s diagnostics exposure=%v", path, hasDetails)
			}
		}
	}
	check("scale", "/api/v1/workers?excludeDeleted=true", 1, false)
	check("admin", "/api/v1/workers?excludeDeleted=true", 1, true)
	check("admin", "/api/v1/workers", 2, true)
}
