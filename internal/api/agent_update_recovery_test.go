package api

import(
 "bytes"
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"

 "github.com/eutelic/runnerhub/internal/agent"
 "github.com/eutelic/runnerhub/internal/agenthub"
 "github.com/eutelic/runnerhub/internal/core"
 "github.com/eutelic/runnerhub/internal/provider"
 "github.com/eutelic/runnerhub/internal/store"
)

func TestRecoveryClaimAuthenticatesAndFencesLateUpdateSuccess(t *testing.T){
 ctx:=context.Background()
 st,err:=store.OpenSQLite(":memory:")
 if err!=nil{t.Fatal(err)}
 defer st.Close()
 hub,err:=agenthub.New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef")
 if err!=nil{t.Fatal(err)}
 enrolled,err:=hub.Enroll(ctx,"0123456789abcdef",agent.EnrollRequest{
  Name:"compute",Provider:"docker",AgentVersion:"old",
  Capabilities:provider.Capabilities{Provider:"docker"},
 })
 if err!=nil{t.Fatal(err)}
 targetImage:="ghcr.io/eutelic/runnerhub-agent@sha256:"+strings.Repeat("a",64)
 revision:=strings.Repeat("b",40)
 lease,_,err:=st.BeginAgentUpdateLease(ctx,store.AgentUpdateLease{
  HostID:enrolled.HostID,OperationID:"update-recoverable",
  TargetImage:targetImage,TargetRevision:revision,
 })
 if err!=nil{t.Fatal(err)}
 if err:=st.TransitionAgentUpdateLease(ctx,lease.HostID,lease.OperationID,
  "draining","replacing","helper-launched");err!=nil{t.Fatal(err)}
 svc:=&Service{Store:st,Hub:hub}
 send:=func(path,token,operationID,outcome string,override ...string)int{
  owner:="0123456789abcdef0123456789abcdef"
  if len(override)>0{owner=override[0]}
  data:=map[string]string{
   "hostId":enrolled.HostID,"operationId":operationID,
  }
  if outcome!=""{data["outcome"]=outcome}
  if strings.Contains(path,"update-recovery-start")||outcome=="rolled-back"{
   data["recoveryOwner"]=owner
  }
  raw,_:=json.Marshal(data)
  req:=httptest.NewRequest(http.MethodPost,path,bytes.NewReader(raw))
  req.Header.Set("Authorization","Bearer "+token)
  req.Header.Set("Content-Type","application/json")
  response:=httptest.NewRecorder()
  svc.Handler().ServeHTTP(response,req)
  return response.Code
 }
 recoveryURL:="/api/v1/agent/update-recovery-start"
 resultURL:="/api/v1/agent/update-result"
 if got:=send(recoveryURL,"invalid",lease.OperationID,"");got!=http.StatusUnauthorized{
  t.Fatalf("unauthenticated recovery claimed: %d",got)
 }
 if got:=send(recoveryURL,enrolled.AgentToken,"wrong-op","");got!=http.StatusNotFound{
  t.Fatalf("wrong operation claimed: %d",got)
 }
 for i:=0;i<2;i++{
  if got:=send(recoveryURL,enrolled.AgentToken,lease.OperationID,"");got!=http.StatusOK{
   t.Fatalf("idempotent recovery claim %d failed: %d",i,got)
  }
 }
 if got:=send(recoveryURL,enrolled.AgentToken,lease.OperationID,"",
  "fedcba9876543210fedcba9876543210");got!=http.StatusConflict{
  t.Fatalf("other recovery helper shared exclusive lease: %d",got)
 }
 if got:=send(resultURL,enrolled.AgentToken,lease.OperationID,"rolled-back",
  "fedcba9876543210fedcba9876543210");got!=http.StatusConflict{
  t.Fatalf("wrong helper completed rollback: %d",got)
 }
 if got:=send(resultURL,enrolled.AgentToken,lease.OperationID,"succeeded");got!=http.StatusConflict{
  t.Fatalf("old updater claimed success after recovery: %d",got)
 }
 if got:=send(resultURL,enrolled.AgentToken,lease.OperationID,"review");got!=http.StatusConflict{
  t.Fatalf("old updater changed recovery into review: %d",got)
 }
 if got:=send(resultURL,enrolled.AgentToken,lease.OperationID,"rolled-back");got!=http.StatusOK{
  t.Fatalf("confirmed rollback not accepted: %d",got)
 }
 host,err:=st.GetHost(ctx,enrolled.HostID)
 if err!=nil||host.State!=core.HostDraining{t.Fatalf("host resumed during rollback: %+v %v",host,err)}
 if got:=send(recoveryURL,enrolled.AgentToken,lease.OperationID,"");got!=http.StatusConflict{
  t.Fatalf("terminal rollback reopened: %d",got)
 }
}
