package api

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "time"

 "github.com/eutelic/runnerhub/internal/agent"
 "github.com/eutelic/runnerhub/internal/agenthub"
 "github.com/eutelic/runnerhub/internal/core"
 "github.com/eutelic/runnerhub/internal/provider"
 "github.com/eutelic/runnerhub/internal/store"
)

func TestAgentUpdateStatusUsesOnlyMatchingHostCredentialAndOperation(t *testing.T){
 ctx:=context.Background()
 st,err:=store.OpenSQLite(":memory:")
 if err!=nil{t.Fatal(err)}
 defer st.Close()
 hub,err:=agenthub.New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef")
 if err!=nil{t.Fatal(err)}
 enrolled,err:=hub.Enroll(ctx,"0123456789abcdef",agent.EnrollRequest{
  Name:"compute",Provider:"docker",AgentVersion:"old",Capabilities:provider.Capabilities{Provider:"docker"},
 })
 if err!=nil{t.Fatal(err)}
 h,err:=st.GetHost(ctx,enrolled.HostID)
 if err!=nil{t.Fatal(err)}
 h.LastSeenAt=time.Now().UTC()
 if err:=st.UpsertHost(ctx,h);err!=nil{t.Fatal(err)}
 targetImage:="ghcr.io/eutelic/runnerhub-agent@sha256:"+strings.Repeat("a",64)
 targetRevision:=strings.Repeat("b",40)
 _,_,err=st.BeginAgentUpdateLease(ctx,store.AgentUpdateLease{
  HostID:h.ID,OperationID:"op-safe",TargetImage:targetImage,TargetRevision:targetRevision,
 })
 if err!=nil{t.Fatal(err)}
 svc:=&Service{Store:st,Hub:hub}
 get:=func(token,op string)(int,AgentUpdateStatus){
  req:=httptest.NewRequest(http.MethodGet,"/api/v1/agent/update-status?host_id="+h.ID+"&operation_id="+op,nil)
  req.Header.Set("Authorization","Bearer "+token)
  rr:=httptest.NewRecorder()
  svc.Handler().ServeHTTP(rr,req)
  var status AgentUpdateStatus
  if rr.Code==http.StatusOK&&json.NewDecoder(rr.Body).Decode(&status)!=nil{t.Fatal("invalid status response")}
  return rr.Code,status
 }
 if code,_:=get("invalid","op-safe");code!=http.StatusUnauthorized{t.Fatalf("invalid token was accepted: %d",code)}
 if code,_:=get(enrolled.AgentToken,"wrong-operation");code!=http.StatusNotFound{t.Fatalf("wrong operation was accepted: %d",code)}
 code,status:=get(enrolled.AgentToken,"op-safe")
 if code!=http.StatusOK||status.CanReplace{t.Fatalf("unacknowledged update must not swap: %d %+v",code,status)}
 if err:=st.TransitionAgentUpdateLease(ctx,h.ID,"op-safe","draining","replacing","helper-launched");err!=nil{t.Fatal(err)}
 code,status=get(enrolled.AgentToken,"op-safe")
 if code!=http.StatusOK||!status.CanReplace||status.HostID!=h.ID||status.OperationID!="op-safe"{
  t.Fatalf("expected safely drained host: %d %+v",code,status)
 }
 if err:=st.UpsertWorker(ctx,core.Worker{ID:"busy",HostID:h.ID,State:core.WorkerBusy});err!=nil{t.Fatal(err)}
 code,status=get(enrolled.AgentToken,"op-safe")
 if code!=http.StatusOK||status.CanReplace{t.Fatalf("busy worker not considered: %d %+v",code,status)}
}
