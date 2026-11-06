package api

import (
    "bytes"
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/eutelic/runnerhub/internal/auth"
    "github.com/eutelic/runnerhub/internal/core"
    "github.com/eutelic/runnerhub/internal/store"
)

func TestAgentUpdatePreflightFailsClosed(t *testing.T) {
    pinned:="ghcr.io/eutelic/runnerhub-agent@sha256:"+strings.Repeat("a",64)
    host:=core.Host{ID:"host-new",Name:"compute",Provider:"docker",State:core.HostOnline,AgentVersion:"old",SystemTags:map[string]string{"feature.agentSelfUpdate":"true"}}
    workers:=[]core.Worker{
        {HostID:host.ID,State:core.WorkerBusy},
        {HostID:host.ID,State:core.WorkerCleanupPending,ProviderHandle:"runner-container"},
        {HostID:host.ID,State:core.WorkerDeleted},
        {HostID:"another-host",State:core.WorkerBusy},
    }
    got:=makeAgentUpdatePreflight(host,workers,pinned)
    if !got.ImmutableTarget || !got.UpdaterAdvertised || got.ActiveWorkers!=2 || got.CleanupReservations!=1 {
        t.Fatalf("unexpected update readiness: %+v",got)
    }
    if got.CanUpdate || !hasBlocker(got,"active-workers-or-cleanup") || !hasBlocker(got,"update-executor-not-implemented") {
        t.Fatalf("unsafe approval: %+v",got)
    }
    got=makeAgentUpdatePreflight(host,nil,"ghcr.io/eutelic/runnerhub-agent:latest")
    if got.ImmutableTarget || !hasBlocker(got,"immutable-verified-target-required") {
        t.Fatalf("moving image accepted: %+v",got)
    }
    host.Provider="process"
    host.State=core.HostOffline
    got=makeAgentUpdatePreflight(host,nil,pinned)
    if !hasBlocker(got,"non-docker-agent") || !hasBlocker(got,"host-not-online") {
        t.Fatalf("unsupported machine accepted: %+v",got)
    }
}
func hasBlocker(p AgentUpdatePreflight, wanted string) bool {
    for _,v:=range p.Blockers {if v==wanted {return true}}
    return false
}

func TestAgentUpdateEndpointsAreAdminOnlyAndNonMutating(t *testing.T) {
    ctx:=context.Background()
    st,err:=store.OpenSQLite(":memory:")
    if err!=nil {t.Fatal(err)}
    defer st.Close()
    h:=core.Host{ID:"host-a",Name:"compute",Provider:"docker",State:core.HostOnline,AgentVersion:"f8517937",LastSeenAt:time.Now().UTC()}
    if err:=st.UpsertHost(ctx,h);err!=nil {t.Fatal(err)}
    keys:=auth.New(auth.Config{APIKeys:auth.ParseKeys("viewer:viewer:test,admin:admin:test")})
    svc:=&Service{Store:st,Auth:keys,MachineAgentImage:"ghcr.io/eutelic/runnerhub-agent:latest"}
    endpoint:="/api/v1/hosts/host-a/agent-update-preflight"
    for _,tt:=range []struct{method,url,token string;status int}{
        {http.MethodGet,endpoint,"viewer",http.StatusForbidden},
        {http.MethodPost,"/api/v1/hosts/host-a/update-agent","viewer",http.StatusForbidden},
        {http.MethodGet,endpoint,"admin",http.StatusOK},
        {http.MethodPost,"/api/v1/hosts/host-a/update-agent","admin",http.StatusServiceUnavailable},
        {http.MethodGet,"/api/v1/hosts/missing/agent-update-preflight","admin",http.StatusNotFound},
    }{
        req:=httptest.NewRequest(tt.method,tt.url,bytes.NewReader(nil))
        req.Header.Set("Authorization","Bearer "+tt.token)
        rr:=httptest.NewRecorder()
        svc.Handler().ServeHTTP(rr,req)
        if rr.Code!=tt.status{t.Fatalf("%s %s as %s = %d, expected %d: %s",tt.method,tt.url,tt.token,rr.Code,tt.status,rr.Body.String())}
        if tt.method==http.MethodGet && tt.url==endpoint && tt.token=="admin"{
            var report AgentUpdatePreflight
            if err:=json.NewDecoder(rr.Body).Decode(&report);err!=nil{t.Fatal(err)}
            if report.HostID!=h.ID || report.CanUpdate || !hasBlocker(report,"update-executor-not-implemented"){
                t.Fatalf("unexpected report: %+v",report)
            }
        }
    }
    unchanged,err:=st.GetHost(ctx,h.ID)
    if err!=nil{t.Fatal(err)}
    if unchanged.State!=core.HostOnline || unchanged.AgentVersion!=h.AgentVersion {
        t.Fatalf("update inspection mutated host: %+v",unchanged)
    }
}

func TestAgentUpdateTargetIsSeparateFromBootstrapTag(t *testing.T){
    image:="ghcr.io/eutelic/runnerhub-agent@sha256:"+strings.Repeat("a",64)
    revision:=strings.Repeat("b",40)
    h:=core.Host{ID:"host-123",Provider:"docker",State:core.HostOnline,SystemTags:map[string]string{"feature.agentSelfUpdate":"true"}}
    bad:=makeAgentUpdatePreflight(h,nil,image)
    if !bad.ImmutableTarget||bad.TargetMetadataValid||!hasBlocker(bad,"verified-release-metadata-required"){t.Fatalf("accepted unverified revision: %+v",bad)}
    valid:=makeAgentUpdatePreflight(h,nil,image,revision)
    if !valid.TargetMetadataValid||valid.ConfiguredTargetRevision!=revision||!valid.ImmutableTarget{t.Fatalf("target metadata not shown: %+v",valid)}
    if valid.CanUpdate||!hasBlocker(valid,"update-executor-not-implemented"){t.Fatalf("executor should remain disabled: %+v",valid)}
}
