package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestPoolHealthAdvertisesLiveCapacityAndRespectsMaintenance(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(":memory:")
	if err != nil { t.Fatal(err) }
	defer db.Close()
	now := time.Now().UTC()
	pool := core.RunnerPool{
		ID: "pool", MaxConcurrency: 4, Isolation:core.IsolationContainerTrusted,
		Resources:core.Resources{CPUMillis:1000, MemoryBytes:1 << 30},
	}
	host := core.Host{
		ID:"compute", Name:"compute", Provider:"docker", State:core.HostOnline,
		LastSeenAt:now, Tags:map[string]string{"isolation.container-trusted":"true"},
		Capacity:core.Resources{CPUMillis:3000,MemoryBytes:4 << 30},
	}
	if err := db.UpsertPool(ctx,pool); err != nil { t.Fatal(err) }
	if err := db.UpsertHost(ctx,host); err != nil { t.Fatal(err) }
	svc := &Service{Store:db}
	readCapacity := func() int {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/pools/pool/health", nil)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w,r)
		if w.Code != http.StatusOK { t.Fatalf("health status=%d: %s",w.Code,w.Body.String()) }
		var v struct{ CapacityLimit int `json:"capacityLimit"`; Healthy bool `json:"healthy"` }
		if err := json.Unmarshal(w.Body.Bytes(),&v); err != nil { t.Fatal(err) }
		return v.CapacityLimit
	}
	if n := readCapacity(); n != 3 { t.Fatalf("initial capacity=%d, want 3",n) }
	other := core.Worker{
		ID:"other",PoolID:"another-pool",HostID:"compute",State:core.WorkerBusy,
		ProviderID:"docker",ProviderHandle:"container-other",
		Isolation:core.IsolationContainerTrusted,
		Resources:core.Resources{CPUMillis:2000,MemoryBytes:2 << 30},
	}
	if err := db.UpsertWorker(ctx,other); err != nil { t.Fatal(err) }
	if n := readCapacity(); n != 1 { t.Fatalf("shared-host capacity=%d, want 1",n) }
	if err := db.SetMaintenance(ctx,store.MaintenanceState{Paused:true,Reason:"safe redeploy",Generation:"test"}); err != nil { t.Fatal(err) }
	if n := readCapacity(); n != 0 { t.Fatalf("paused admission advertised spare capacity=%d",n) }
	if err := db.SetMaintenance(ctx,store.MaintenanceState{Paused:false}); err != nil { t.Fatal(err) }
	if n := readCapacity(); n != 1 { t.Fatalf("resumed capacity=%d, want 1",n) }
}
