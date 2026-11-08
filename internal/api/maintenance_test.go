package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/scheduler"
	"github.com/eutelic/runnerhub/internal/store"
)

func maintenanceRequest(handler http.Handler, method, path, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(payload))
	req.Header.Set("Authorization", "Bearer maintenance-test-key")
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestMaintenanceBlocksNewAdmissionsAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runnerhub.db")
	st, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st, Scheduler: scheduler.Scheduler{Store: st},
		Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{
			"maintenance-test-key": {Name: "test-admin", Role: auth.RoleAdmin},
		}}),
	}
	handler := svc.Handler()
	initial := maintenanceRequest(handler, http.MethodGet, "/api/v1/maintenance", "")
	if initial.Code != 200 {
		t.Fatalf("initial %d %s", initial.Code, initial.Body.String())
	}
	if !strings.Contains(initial.Body.String(), "\"paused\":false") {
		t.Fatal(initial.Body.String())
	}
	if err := st.UpsertWorker(context.Background(), core.Worker{ID: "w1", State: core.WorkerBusy}); err != nil {
		t.Fatal(err)
	}
	paused := maintenanceRequest(handler, http.MethodPost, "/api/v1/maintenance/pause", "")
	if paused.Code != 202 {
		t.Fatalf("pause %d: %s", paused.Code, paused.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(paused.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	generation, _ := status["generation"].(string)
	if status["paused"] != true || generation == "" || status["activeWorkers"] != float64(1) || status["phase"] != "draining" {
		t.Fatalf("%+v", status)
	}
	if _, err := svc.enqueueWorker(context.Background(), core.RunnerPool{}, "after-pause", 0, "config", "", ""); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("new worker accepted while paused: %v", err)
	}
	again := maintenanceRequest(handler, http.MethodPost, "/api/v1/maintenance/pause", "")
	if again.Code != 409 {
		t.Fatalf("repeat pause %d", again.Code)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err := reopened.GetMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Paused || stored.Generation != generation {
		t.Fatalf("pause not durable: %+v", stored)
	}
	svc.Store = reopened
	if err := reopened.UpsertWorker(context.Background(), core.Worker{ID: "w1", State: core.WorkerDeleted}); err != nil {
		t.Fatal(err)
	}
	ready := maintenanceRequest(handler, http.MethodGet, "/api/v1/maintenance", "")
	if ready.Code != 200 || !strings.Contains(ready.Body.String(), "\"phase\":\"ready_to_redeploy\"") {
		t.Fatalf("not drained: %s", ready.Body.String())
	}
	mismatch := maintenanceRequest(handler, http.MethodPost, "/api/v1/maintenance/resume", "{\"generation\":\"other\"}")
	if mismatch.Code != 409 {
		t.Fatalf("wrong generation accepted: %d", mismatch.Code)
	}
	body, _ := json.Marshal(map[string]string{"generation": generation})
	resumed := maintenanceRequest(handler, http.MethodPost, "/api/v1/maintenance/resume", string(body))
	if resumed.Code != 200 || !strings.Contains(resumed.Body.String(), "\"paused\":false") {
		t.Fatalf("resume %d: %s", resumed.Code, resumed.Body.String())
	}
}

func TestMaintenancePauseRequiresAdmin(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := &Service{Store: st, Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{
		"maintenance-test-key": {Name: "viewer", Role: auth.RoleViewer},
	}})}
	res := maintenanceRequest(svc.Handler(), http.MethodPost, "/api/v1/maintenance/pause", "")
	if res.Code != 403 {
		t.Fatalf("viewer paused admission: %d", res.Code)
	}
}
