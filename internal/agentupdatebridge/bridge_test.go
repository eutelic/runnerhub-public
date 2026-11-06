package agentupdatebridge

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"
)

func bridgeTarget()(string,string){
 return "ghcr.io/eutelic/runnerhub-agent@sha256:"+strings.Repeat("a",64),strings.Repeat("b",40)
}
func bridgeCredential(t *testing.T)string{
 t.Helper()
 p:=filepath.Join(t.TempDir(),"agent-credentials.json")
 if err:=os.WriteFile(p,[]byte(`{"hostId":"host-compute","token":"host-secret"}`),0600);err!=nil{t.Fatal(err)}
 return p
}

func TestBridgeEnforcesExactLeaseAndBearerIdentity(t *testing.T){
 image,revision:=bridgeTarget()
 called:=0
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  called++
  if r.Header.Get("Authorization")!="Bearer host-secret"||r.URL.Query().Get("host_id")!="host-compute"||
   r.URL.Query().Get("operation_id")!="op-123"{
   t.Errorf("bridge did not authenticate exact host and operation")
   w.WriteHeader(http.StatusUnauthorized);return
  }
  _=json.NewEncoder(w).Encode(Status{
   HostID:"host-compute",OperationID:"op-123",OperationState:"replacing",
   TargetImage:image,TargetRevision:revision,
   ObservedRevision:revision,HeartbeatAt:time.Now().UTC(),CanReplace:true,
  })
 }))
 defer srv.Close()
 bridge,err:=New(Config{ControlURL:srv.URL,HostID:"host-compute",OperationID:"op-123",
  CredentialFile:bridgeCredential(t),TargetImage:image,TargetRevision:revision})
 if err!=nil{t.Fatal(err)}
 if err:=bridge.CheckDrained(context.Background(),"host-compute");err!=nil{t.Fatal(err)}
 if err:=bridge.WaitForHeartbeat(context.Background(),"host-compute",revision,time.Now().Add(-time.Minute));err!=nil{t.Fatal(err)}
 if called!=2{t.Fatalf("unexpected bridge requests %d",called)}
 if err:=bridge.CheckDrained(context.Background(),"wrong-host");err==nil{t.Fatal("wrong host permitted")}
}

func TestBridgeBlocksWrongReleaseOrState(t *testing.T){
 image,revision:=bridgeTarget()
 state:="draining"
 remoteImage:=image
 ready:=true
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  _=json.NewEncoder(w).Encode(Status{
   HostID:"host-compute",OperationID:"op-123",OperationState:state,
   TargetImage:remoteImage,TargetRevision:revision,
   ObservedRevision:"old",HeartbeatAt:time.Now().UTC(),CanReplace:ready,
  })
 }))
 defer srv.Close()
 bridge,err:=New(Config{ControlURL:srv.URL,HostID:"host-compute",OperationID:"op-123",
  CredentialFile:bridgeCredential(t),TargetImage:image,TargetRevision:revision})
 if err!=nil{t.Fatal(err)}
 state="review"
 if err:=bridge.CheckDrained(context.Background(),"host-compute");err==nil{t.Fatal("ambiguous update permitted")}
 state="draining";ready=false
 if err:=bridge.CheckDrained(context.Background(),"host-compute");err==nil{t.Fatal("busy host permitted")}
 ready=true;remoteImage="ghcr.io/eutelic/runnerhub-agent@sha256:"+strings.Repeat("c",64)
 if err:=bridge.CheckDrained(context.Background(),"host-compute");err==nil{t.Fatal("mutable release accepted")}
}

func TestBridgeRejectsRemotePlaintextWithoutOptIn(t *testing.T){
 image,revision:=bridgeTarget()
 _,err:=New(Config{ControlURL:"http://compute.example.com",HostID:"host-compute",
  OperationID:"op-123",TargetImage:image,TargetRevision:revision,CredentialFile:"credentials.json"})
 if err==nil{t.Fatal("unencrypted remote control plane allowed")}
}

func TestBridgeRecoveryClaimIsAuthenticatedAndAtomic(t *testing.T){
 image,revision:=bridgeTarget()
 state:="replacing"
 claims:=0
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.Header.Get("Authorization")!="Bearer host-secret"{
   w.WriteHeader(http.StatusUnauthorized);return
  }
  if r.Method==http.MethodGet{
   _=json.NewEncoder(w).Encode(Status{
    HostID:"host-compute",OperationID:"op-123",OperationState:state,
    TargetImage:image,TargetRevision:revision,
   })
   return
  }
  if r.Method!=http.MethodPost||r.URL.Path!="/api/v1/agent/update-recovery-start"{
   w.WriteHeader(http.StatusNotFound);return
  }
  claims++
  var body struct{
   HostID string `json:"hostId"`
   OperationID string `json:"operationId"`
   RecoveryOwner string `json:"recoveryOwner"`
  }
  if json.NewDecoder(r.Body).Decode(&body)!=nil||
   body.HostID!="host-compute"||body.OperationID!="op-123"||
   body.RecoveryOwner!="0123456789abcdef0123456789abcdef"{
   w.WriteHeader(http.StatusBadRequest);return
  }
  state="recovering"
  _=json.NewEncoder(w).Encode(map[string]any{
   "hostId":"host-compute","operationId":"op-123","state":state,
   "recoveryOwner":"0123456789abcdef0123456789abcdef",
  })
 }))
 defer srv.Close()
 b,err:=New(Config{ControlURL:srv.URL,HostID:"host-compute",OperationID:"op-123",
  CredentialFile:bridgeCredential(t),TargetImage:image,TargetRevision:revision,
  RecoveryOwner:"0123456789abcdef0123456789abcdef"})
 if err!=nil{t.Fatal(err)}
 for i:=0;i<2;i++{
  if err:=b.ClaimRecovery(context.Background(),"host-compute");err!=nil{t.Fatal(err)}
 }
 if claims!=2{t.Fatalf("unexpected claim count %d",claims)}
 if err:=b.ClaimRecovery(context.Background(),"another-host");err==nil{
  t.Fatal("mismatched recovery host was permitted")
 }
 state="succeeded"
 if err:=b.ClaimRecovery(context.Background(),"host-compute");err==nil{
  t.Fatal("server-completed update allowed rollback recovery")
 }
}
