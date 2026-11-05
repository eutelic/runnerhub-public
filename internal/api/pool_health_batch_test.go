package api

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/eutelic/runnerhub/internal/core"
    "github.com/eutelic/runnerhub/internal/fleet"
    "github.com/eutelic/runnerhub/internal/store"
)

func TestPoolHealthBatchUsesLiveHostCapacityAcrossPools(t *testing.T) {
    ctx:=context.Background()
    db,err:=store.OpenSQLite(":memory:")
    if err!=nil {t.Fatal(err)}
    defer db.Close()
    now:=time.Now().UTC()
    for _,id:=range []string{"standard","publisher"} {
        pool:=core.RunnerPool{
            ID:id,Name:id,Scope:core.GitHubScope{Kind:core.ScopeRepository,Owner:"eutelic",Repository:id},
            Labels:[]string{"runnerhub"},Isolation:core.IsolationContainerTrusted,
            Resources:core.Resources{CPUMillis:1000,MemoryBytes:1<<30,DiskBytes:1<<30},
            MaxConcurrency:3,RunnerGroupID:1,GitHubInstallationID:42,
        }
        if err:=db.UpsertPool(ctx,pool);err!=nil {t.Fatal(err)}
    }
    host:=core.Host{
        ID:"compute",Name:"compute",Provider:"docker",State:core.HostOnline,
        LastSeenAt:now,Tags:map[string]string{"isolation.container-trusted":"true"},
        Capacity:core.Resources{CPUMillis:4000,MemoryBytes:8<<30,DiskBytes:50<<30},
    }
    if err:=db.UpsertHost(ctx,host);err!=nil {t.Fatal(err)}
    busy:=core.Worker{
        ID:"w1",HostID:host.ID,PoolID:"standard",State:core.WorkerBusy,
        ProviderID:"docker",ProviderHandle:"container",Isolation:core.IsolationContainerTrusted,
        Resources:core.Resources{CPUMillis:3500,MemoryBytes:3<<30,DiskBytes:1<<30},
    }
    if err:=db.UpsertWorker(ctx,busy);err!=nil {t.Fatal(err)}
    if err:=db.UpsertWorker(ctx,core.Worker{
        ID:"old",HostID:host.ID,PoolID:"publisher",State:core.WorkerDeleted,
        Resources:core.Resources{CPUMillis:100000},
    });err!=nil {t.Fatal(err)}
    service:=&Service{Store:db}
    fetch:=func() map[string]fleet.Health {
        t.Helper()
        req:=httptest.NewRequest(http.MethodGet,"/api/v1/pools/health",nil)
        res:=httptest.NewRecorder()
        service.Handler().ServeHTTP(res,req)
        if res.Code!=http.StatusOK {t.Fatalf("status=%d body=%s",res.Code,res.Body.String())}
        var rows []fleet.Health
        if err:=json.Unmarshal(res.Body.Bytes(),&rows);err!=nil {t.Fatal(err)}
        if len(rows)!=2 {t.Fatalf("expected two pools: %+v",rows)}
        byID:=map[string]fleet.Health{}
        for _,row:=range rows {byID[row.PoolID]=row}
        return byID
    }
    first:=fetch()
    if first["standard"].ActiveWorkers!=1 || first["publisher"].ActiveWorkers!=0 ||
        first["publisher"].Healthy || first["publisher"].OnlineHosts!=0 {
        t.Fatalf("expected cross-pool capacity blocker: %+v",first)
    }
    if !strings.Contains(strings.ToLower(strings.Join(first["publisher"].Reasons,";")),"cpu"){
        t.Fatalf("missing CPU reason: %+v",first["publisher"])
    }
    busy.State=core.WorkerDeleted
    busy.UpdatedAt=now
    if err:=db.UpsertWorker(ctx,busy);err!=nil {t.Fatal(err)}
    after:=fetch()
    if !after["standard"].Healthy || !after["publisher"].Healthy ||
        after["publisher"].OnlineHosts!=1 || after["standard"].ActiveWorkers!=0 {
        t.Fatalf("capacity release not reflected: %+v",after)
    }
}
