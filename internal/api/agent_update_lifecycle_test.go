package api

import(
 "bytes"
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "time"

 "github.com/eutelic/runnerhub/internal/agent"
 "github.com/eutelic/runnerhub/internal/agenthub"
 "github.com/eutelic/runnerhub/internal/auth"
 "github.com/eutelic/runnerhub/internal/core"
 "github.com/eutelic/runnerhub/internal/provider"
 "github.com/eutelic/runnerhub/internal/store"
)

func TestOneClickUpdateControlLifecycleWithoutDocker(t *testing.T){
 ctx:=context.Background()
 st,err:=store.OpenSQLite(":memory:")
 if err!=nil{t.Fatal(err)}
 defer st.Close()
 hub,err:=agenthub.New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef")
 if err!=nil{t.Fatal(err)}
 caps:=provider.Capabilities{Provider:"docker",Features:map[string]bool{"agentSelfUpdate":true}}
 enrolled,err:=hub.Enroll(ctx,"0123456789abcdef",agent.EnrollRequest{
  Name:"compute",Provider:"docker",AgentVersion:"old",Capabilities:caps,
 })
 if err!=nil{t.Fatal(err)}
 targetImage:="ghcr.io/eutelic/runnerhub-agent@sha256:"+strings.Repeat("a",64)
 targetRevision:=strings.Repeat("b",40)
 helperImage:="ghcr.io/eutelic/runnerhub-agent-updater@sha256:"+strings.Repeat("c",64)
 svc:=&Service{
  Store:st,Hub:hub,Auth:auth.New(auth.Config{APIKeys:auth.ParseKeys("admin:admin:operator,viewer:viewer:test")}),
  AgentUpdateEnabled:true,AgentUpdateTargetImage:targetImage,AgentUpdateTargetRevision:targetRevision,
  AgentUpdateHelperImage:helperImage,
 }
 hub.SetResultHandler(svc.HandleAgentResult)
 request:=func(method,url,token string,body []byte)*httptest.ResponseRecorder{
  r:=httptest.NewRequest(method,url,bytes.NewReader(body))
  r.Header.Set("Authorization","Bearer "+token)
  if len(body)>0{r.Header.Set("Content-Type","application/json")}
  w:=httptest.NewRecorder()
  svc.Handler().ServeHTTP(w,r)
  return w
 }
 url:="/api/v1/hosts/"+enrolled.HostID+"/update-agent"
 if got:=request(http.MethodPost,url,"viewer",nil);got.Code!=http.StatusForbidden{
  t.Fatalf("viewer could begin maintenance: %d",got.Code)
 }
 pre:=request(http.MethodGet,"/api/v1/hosts/"+enrolled.HostID+"/agent-update-preflight","admin",nil)
 if pre.Code!=http.StatusOK||!bytes.Contains(pre.Body.Bytes(),[]byte(`"canUpdate":true`)){
  t.Fatalf("ready Docker host not eligible: %d %s",pre.Code,pre.Body.String())
 }
 reply:=request(http.MethodPost,url,"admin",nil)
 if reply.Code!=http.StatusAccepted{t.Fatalf("start: %d %s",reply.Code,reply.Body.String())}
 var accepted struct{
  Operation store.AgentUpdateLease `json:"operation"`
  Reused bool `json:"reused"`
 }
 if err:=json.NewDecoder(reply.Body).Decode(&accepted);err!=nil{t.Fatal(err)}
 if accepted.Operation.HostID!=enrolled.HostID||accepted.Operation.State!="draining"||accepted.Reused{
  t.Fatalf("bad operation %+v",accepted)
 }
 host,_:=st.GetHost(ctx,enrolled.HostID)
 if host.State!=core.HostDraining{t.Fatal("host not drained before update command")}
 duplicate:=request(http.MethodPost,url,"admin",nil)
 if duplicate.Code!=http.StatusAccepted||!bytes.Contains(duplicate.Body.Bytes(),[]byte(`"reused":true`)){
  t.Fatalf("duplicate update was not idempotent: %d %s",duplicate.Code,duplicate.Body.String())
 }
 var command agent.Command
 for deadline:=time.Now().Add(3*time.Second);time.Now().Before(deadline);{
  cmds:=hub.Next(enrolled.HostID)
  if len(cmds)>0{command=cmds[0];break}
  time.Sleep(20*time.Millisecond)
 }
 if command.Type!=agent.CommandUpdateAgent||command.AgentUpdate==nil{
  t.Fatal("detached helper command was not queued")
 }
 if command.AgentUpdate.HostID!=enrolled.HostID||command.AgentUpdate.TargetImage!=targetImage||
  command.AgentUpdate.HelperImage!=helperImage||command.WorkerID!=accepted.Operation.OperationID{
  t.Fatalf("wrong helper command: %+v",command)
 }
 statusURL:="/api/v1/agent/update-status?host_id="+enrolled.HostID+"&operation_id="+accepted.Operation.OperationID
 beforeAck:=request(http.MethodGet,statusURL,enrolled.AgentToken,nil)
 if beforeAck.Code!=http.StatusOK||!bytes.Contains(beforeAck.Body.Bytes(),[]byte(`"canReplace":false`)){
  t.Fatalf("helper was armed before agent ack: %d %s",beforeAck.Code,beforeAck.Body.String())
 }
 res:=agent.CommandResult{CommandID:command.ID,WorkerID:command.WorkerID,OK:true,At:time.Now().UTC()}
 body,_:=json.Marshal(res)
 ack:=request(http.MethodPost,"/api/v1/agent/results",enrolled.AgentToken,body)
 if ack.Code!=http.StatusNoContent{t.Fatalf("launch ack: %d %s",ack.Code,ack.Body.String())}
 armed:=request(http.MethodGet,statusURL,enrolled.AgentToken,nil)
 if armed.Code!=http.StatusOK||!bytes.Contains(armed.Body.Bytes(),[]byte(`"canReplace":true`)){
  t.Fatalf("helper was not armed: %d %s",armed.Code,armed.Body.String())
 }
 // The replacement retains exactly the enrolled host ID, and the control
 // plane observes the new revision via the standard authenticated heartbeat.
 hb:=agent.Heartbeat{HostID:enrolled.HostID,AgentVersion:targetRevision,At:time.Now().UTC(),Capabilities:caps}
 body,_=json.Marshal(hb)
 heartbeat:=request(http.MethodPost,"/api/v1/agent/heartbeat",enrolled.AgentToken,body)
 if heartbeat.Code!=http.StatusNoContent{t.Fatalf("new heartbeat: %d %s",heartbeat.Code,heartbeat.Body.String())}
 body,_=json.Marshal(map[string]string{"hostId":enrolled.HostID,"operationId":accepted.Operation.OperationID,"outcome":"succeeded"})
 complete:=request(http.MethodPost,"/api/v1/agent/update-result",enrolled.AgentToken,body)
 if complete.Code!=http.StatusOK{t.Fatalf("helper completion: %d %s",complete.Code,complete.Body.String())}
 final,_:=st.GetHost(ctx,enrolled.HostID)
 if final.State!=core.HostOnline||final.AgentVersion!=targetRevision{
  t.Fatalf("verified host not resumed: %+v",final)
 }
 op:=request(http.MethodGet,url,"admin",nil)
 if op.Code!=http.StatusOK||!bytes.Contains(op.Body.Bytes(),[]byte(`"state":"succeeded"`)){
  t.Fatalf("persisted update not visible: %d %s",op.Code,op.Body.String())
 }
}

func TestAgentUpdateLaunchAckIsIdempotentAcrossPartialPersistence(t *testing.T){
 ctx:=context.Background()
 st,err:=store.OpenSQLite(":memory:")
 if err!=nil{t.Fatal(err)}
 defer st.Close()
 host:=core.Host{ID:"host-idempotent",Provider:"docker",State:core.HostOnline,AgentVersion:"old",LastSeenAt:time.Now().UTC()}
 if err:=st.UpsertHost(ctx,host);err!=nil{t.Fatal(err)}
 image:="ghcr.io/eutelic/runnerhub-agent@sha256:"+strings.Repeat("a",64)
 revision:=strings.Repeat("b",40)
 l,_,err:=st.BeginAgentUpdateLease(ctx,store.AgentUpdateLease{
  HostID:host.ID,OperationID:"update-repeat",TargetImage:image,TargetRevision:revision,
 })
 if err!=nil{t.Fatal(err)}
 if err:=st.TransitionAgentUpdateLease(ctx,host.ID,l.OperationID,"draining","dispatching","dispatching-helper");err!=nil{t.Fatal(err)}
 rec:=store.CommandRecord{HostID:host.ID,Command:agent.Command{
  WorkerID:l.OperationID,Type:agent.CommandUpdateAgent,
  AgentUpdate:&agent.AgentUpdateSpec{HostID:host.ID,OperationID:l.OperationID,TargetImage:image,TargetRevision:revision},
 }}
 result:=agent.CommandResult{WorkerID:l.OperationID,OK:true}
 svc:=&Service{Store:st}
 if err:=svc.handleAgentUpdaterLaunchResult(ctx,rec,host.ID,result);err!=nil{t.Fatal(err)}
 // A crash before AckCommand can redeliver exactly this command.
 if err:=svc.handleAgentUpdaterLaunchResult(ctx,rec,host.ID,result);err!=nil{t.Fatalf("duplicate ack failed: %v",err)}
 recorded,err:=st.GetAgentUpdateLease(ctx,host.ID)
 if err!=nil||recorded.State!="replacing"{t.Fatalf("update was not armed once: %+v %v",recorded,err)}
 wrong:=result;wrong.WorkerID="other-update"
 if err:=svc.handleAgentUpdaterLaunchResult(ctx,rec,host.ID,wrong);err==nil{t.Fatal("different operation accepted")}
}

func TestAgentUpdateRecoveryOnlyResumesMatchingPinnedDrain(t *testing.T){
 ctx:=context.Background()
 st,err:=store.OpenSQLite(":memory:")
 if err!=nil{t.Fatal(err)}
 defer st.Close()
 hub,err:=agenthub.New(st,"0123456789abcdef","0123456789abcdef0123456789abcdef")
 if err!=nil{t.Fatal(err)}
 enrolled,err:=hub.Enroll(ctx,"0123456789abcdef",agent.EnrollRequest{
  Name:"compute",Provider:"docker",AgentVersion:"old",Capabilities:provider.Capabilities{
   Provider:"docker",Features:map[string]bool{"agentSelfUpdate":true},
  },
 })
 if err!=nil{t.Fatal(err)}
 image:="ghcr.io/eutelic/runnerhub-agent@sha256:"+strings.Repeat("a",64)
 revision:=strings.Repeat("b",40)
 helper:="ghcr.io/eutelic/runnerhub-agent-updater@sha256:"+strings.Repeat("c",64)
 l,_,err:=st.BeginAgentUpdateLease(ctx,store.AgentUpdateLease{
  HostID:enrolled.HostID,OperationID:"update-recovered",TargetImage:image,TargetRevision:revision,HelperImage:helper,
 })
 if err!=nil{t.Fatal(err)}
 svc:=&Service{Store:st,Hub:hub,AgentUpdateEnabled:true,AgentUpdateTargetImage:image,
  AgentUpdateTargetRevision:revision,AgentUpdateHelperImage:helper}
 if err:=svc.RecoverAgentUpdates(ctx);err!=nil{t.Fatal(err)}
 var got agent.Command
 for deadline:=time.Now().Add(3*time.Second);time.Now().Before(deadline);{
  cmds:=hub.Next(enrolled.HostID)
  if len(cmds)>0{got=cmds[0];break}
  time.Sleep(20*time.Millisecond)
 }
 if got.Type!=agent.CommandUpdateAgent||got.WorkerID!=l.OperationID{
  t.Fatalf("restart recovery did not resume original operation: %+v",got)
 }
 lease,err:=st.GetAgentUpdateLease(ctx,l.HostID)
 if err!=nil||lease.State!="dispatching"{t.Fatalf("recovered stage: %+v %v",lease,err)}
}
