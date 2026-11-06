package agentupdategate

import(
 "context"
 "testing"
 "time"
 "github.com/eutelic/runnerhub/internal/core"
 "github.com/eutelic/runnerhub/internal/store"
)

func TestGateRequiresFreshDrainedHostWithoutWorkers(t *testing.T){
 ctx:=context.Background()
 st,err:=store.OpenSQLite(":memory:")
 if err!=nil{t.Fatal(err)}
 defer st.Close()
 now:=time.Date(2026,11,5,18,0,0,0,time.UTC)
 host:=core.Host{ID:"host-one",Provider:"docker",State:core.HostDraining,LastSeenAt:now.Add(-time.Second)}
 if err:=st.UpsertHost(ctx,host);err!=nil{t.Fatal(err)}
 g:=Gate{Store:st,Now:func()time.Time{return now}}
 if err:=g.CheckDrained(ctx,host.ID);err!=nil{t.Fatalf("idle host rejected: %v",err)}
 if err:=st.UpsertWorker(ctx,core.Worker{ID:"unrelated",HostID:"other-host",State:core.WorkerBusy});err!=nil{t.Fatal(err)}
 if err:=g.CheckDrained(ctx,host.ID);err!=nil{t.Fatalf("unrelated worker rejected: %v",err)}
 if err:=st.UpsertWorker(ctx,core.Worker{ID:"worker-1",HostID:host.ID,State:core.WorkerBusy});err!=nil{t.Fatal(err)}
 if err:=g.CheckDrained(ctx,host.ID);err==nil{t.Fatal("busy worker allowed")}
 if err:=st.UpsertWorker(ctx,core.Worker{ID:"worker-1",HostID:host.ID,State:core.WorkerCleanupPending,ProviderHandle:"docker-id"});err!=nil{t.Fatal(err)}
 if err:=g.CheckDrained(ctx,host.ID);err==nil{t.Fatal("cleanup reservation allowed")}
 if err:=st.UpsertWorker(ctx,core.Worker{ID:"worker-1",HostID:host.ID,State:core.WorkerFailed,ProviderHandle:"docker-id"});err!=nil{t.Fatal(err)}
 if err:=g.CheckDrained(ctx,host.ID);err==nil{t.Fatal("failed worker with live provider handle allowed")}
 if err:=st.UpsertWorker(ctx,core.Worker{ID:"worker-1",HostID:host.ID,State:core.WorkerDeleted});err!=nil{t.Fatal(err)}
 if err:=g.CheckDrained(ctx,host.ID);err!=nil{t.Fatalf("completed cleanup rejected: %v",err)}
}

func TestGateFailsClosedOnHostStateAndHeartbeat(t *testing.T){
 ctx:=context.Background()
 st,err:=store.OpenSQLite(":memory:")
 if err!=nil{t.Fatal(err)}
 defer st.Close()
 now:=time.Date(2026,11,5,18,0,0,0,time.UTC)
 host:=core.Host{ID:"host-one",Provider:"docker",State:core.HostOnline,LastSeenAt:now}
 g:=Gate{Store:st,Now:func()time.Time{return now}}
 if err:=st.UpsertHost(ctx,host);err!=nil{t.Fatal(err)}
 if err:=g.CheckDrained(ctx,host.ID);err==nil{t.Fatal("online non-draining host allowed")}
 host.State=core.HostDraining;host.LastSeenAt=now.Add(-90*time.Second)
 if err:=st.UpsertHost(ctx,host);err!=nil{t.Fatal(err)}
 if err:=g.CheckDrained(ctx,host.ID);err==nil{t.Fatal("stale host allowed")}
 host.LastSeenAt=now.Add(20*time.Second)
 if err:=st.UpsertHost(ctx,host);err!=nil{t.Fatal(err)}
 if err:=g.CheckDrained(ctx,host.ID);err==nil{t.Fatal("future heartbeat allowed")}
 host.LastSeenAt=now;host.Provider="process"
 if err:=st.UpsertHost(ctx,host);err!=nil{t.Fatal(err)}
 if err:=g.CheckDrained(ctx,host.ID);err==nil{t.Fatal("non-Docker host allowed")}
 if err:=g.CheckDrained(ctx,"missing-host");err==nil{t.Fatal("unknown identity allowed")}
}
