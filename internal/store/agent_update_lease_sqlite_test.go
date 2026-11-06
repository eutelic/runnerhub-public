//go:build !localtest

package store

import(
 "context"
 "errors"
 "path/filepath"
 "strings"
 "testing"
 "github.com/eutelic/runnerhub/internal/core"
)

func leaseTarget(hostID,id string)AgentUpdateLease{
 return AgentUpdateLease{
  HostID:hostID,OperationID:id,
  TargetImage:"ghcr.io/eutelic/runnerhub-agent@sha256:"+strings.Repeat("a",64),
  TargetRevision:strings.Repeat("b",40),
 }
}

func TestAgentUpdateLeaseDrainsExactHostAndRejectsDoubleStart(t *testing.T){
 ctx:=context.Background()
 st,err:=OpenSQLite(":memory:")
 if err!=nil{t.Fatal(err)};defer st.Close()
 for _,h:=range []core.Host{
  {ID:"host-compute",Name:"compute",Provider:"docker",State:core.HostOnline},
  {ID:"host-truenas",Name:"truenas",Provider:"docker",State:core.HostOnline},
 } {if err:=st.UpsertHost(ctx,h);err!=nil{t.Fatal(err)}}
 op:=leaseTarget("host-compute","op-1")
 created,reused,err:=st.BeginAgentUpdateLease(ctx,op)
 if err!=nil||reused||created.State!="draining"{t.Fatalf("created=%+v reused=%v err=%v",created,reused,err)}
 h,err:=st.GetHost(ctx,"host-compute")
 if err!=nil||h.State!=core.HostDraining{t.Fatalf("not drained: %+v %v",h,err)}
 other,_:=st.GetHost(ctx,"host-truenas")
 if other.State!=core.HostOnline{t.Fatalf("unrelated host changed: %+v",other)}
 repeat,wasReused,err:=st.BeginAgentUpdateLease(ctx,op)
 if err!=nil||!wasReused||repeat.OperationID!="op-1"{t.Fatalf("idempotency failed: %+v %v %v",repeat,wasReused,err)}
 _,_,err=st.BeginAgentUpdateLease(ctx,leaseTarget("host-compute","op-2"))
 if !errors.Is(err,ErrAgentUpdateBusy){t.Fatalf("parallel update accepted: %v",err)}
 _,reused,err=st.BeginAgentUpdateLease(ctx,leaseTarget("host-truenas","op-another"))
 if err!=nil||reused{t.Fatalf("unrelated host falsely blocked: %v %v",reused,err)}
}

func TestAgentUpdateLeaseRejectsUnsupportedAndInvalidTargets(t *testing.T){
 ctx:=context.Background()
 st,err:=OpenSQLite(":memory:")
 if err!=nil{t.Fatal(err)};defer st.Close()
 for _,h:=range []core.Host{
  {ID:"native",Provider:"process",State:core.HostOnline},
  {ID:"offline",Provider:"docker",State:core.HostOffline},
  {ID:"safe",Provider:"docker",State:core.HostOnline},
 }{if err:=st.UpsertHost(ctx,h);err!=nil{t.Fatal(err)}}
 for _,hostID:=range []string{"native","offline","missing"}{
  _,_,err:=st.BeginAgentUpdateLease(ctx,leaseTarget(hostID,"op"))
  if err==nil{t.Fatalf("unsupported host %s accepted",hostID)}
 }
 bad:=leaseTarget("safe","op")
 bad.TargetImage="ghcr.io/eutelic/runnerhub-agent:latest"
 if _,_,err:=st.BeginAgentUpdateLease(ctx,bad);err==nil{t.Fatal("moving tag accepted")}
 host,_:=st.GetHost(ctx,"safe")
 if host.State!=core.HostOnline{t.Fatal("failed preflight drained host")}
}

func TestAgentUpdateLeaseCASAndUnresolvedStateSurviveRestart(t *testing.T){
 path:=filepath.Join(t.TempDir(),"runnerhub.sqlite")
 ctx:=context.Background()
 st,err:=OpenSQLite(path)
 if err!=nil{t.Fatal(err)}
 if err:=st.UpsertHost(ctx,core.Host{ID:"host-1",Provider:"docker",State:core.HostOnline});err!=nil{t.Fatal(err)}
 _,_,err=st.BeginAgentUpdateLease(ctx,leaseTarget("host-1","op-1"))
 if err!=nil{t.Fatal(err)}
 if err:=st.TransitionAgentUpdateLease(ctx,"host-1","op-1","draining","review","rollback-unverified");err!=nil{t.Fatal(err)}
 if err:=st.TransitionAgentUpdateLease(ctx,"host-1","op-1","draining","succeeded","incorrect");!errors.Is(err,ErrAgentUpdateChanged){t.Fatalf("stale transition: %v",err)}
 if err:=st.Close();err!=nil{t.Fatal(err)}
 st,err=OpenSQLite(path)
 if err!=nil{t.Fatal(err)};defer st.Close()
 saved,err:=st.GetAgentUpdateLease(ctx,"host-1")
 if err!=nil||saved.State!="review"||saved.Step!="rollback-unverified"{t.Fatalf("recovery lost: %+v %v",saved,err)}
 _,_,err=st.BeginAgentUpdateLease(ctx,leaseTarget("host-1","op-2"))
 if !errors.Is(err,ErrAgentUpdateBusy){t.Fatalf("restart unlocked ambiguous agent: %v",err)}
 host,_:=st.GetHost(ctx,"host-1")
 if host.State!=core.HostDraining{t.Fatalf("restart resumed unsafe agent: %+v",host)}
}
