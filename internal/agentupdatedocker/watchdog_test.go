package agentupdatedocker

import(
 "context"
 "strings"
 "testing"
)

func installedWatchdogFixture(cli *fakeCLI,operationID,hostID string)*dockerInspect{
 d:=fixture()
 d.ID=strings.Repeat("e",64)
 d.Name="/runnerhub-agent-watchdog-"+operationID
 d.Config.Image=updaterRepository+"@sha256:"+strings.Repeat("f",64)
 d.Config.Labels=map[string]string{
  "io.runnerhub.update.host":hostID,
  "io.runnerhub.update.operation":operationID,
  "io.runnerhub.update.role":"watchdog",
 }
 d.State.Running=true
 d.HostConfig.RestartPolicy.Name="unless-stopped"
 cli.containers[d.ID]=&d
 return cli.containers[d.ID]
}
func TestRetireWatchdogDisablesRestartOnlyForExactRunningContainer(t *testing.T){
 cli:=newFakeCLI()
 d:=installedWatchdogFixture(cli,"update-123","host-compute")
 a:=&Adapter{Docker:cli,OperationID:"update-123"}
 if err:=a.RetireWatchdog(context.Background(),"host-compute");err!=nil{t.Fatal(err)}
 if d.HostConfig.RestartPolicy.Name!="no"{
  t.Fatalf("watchdog would restart indefinitely: %+v",d.HostConfig.RestartPolicy)
 }
 if cli.containers[oldID]==nil||!cli.containers[oldID].State.Running{
  t.Fatal("watchdog retirement touched original running agent")
 }
}
func TestRetireWatchdogRejectsStaleOrDifferentContainer(t *testing.T){
 cli:=newFakeCLI()
 d:=installedWatchdogFixture(cli,"update-123","host-compute")
 a:=&Adapter{Docker:cli,OperationID:"update-123"}
 d.Config.Labels["io.runnerhub.update.host"]="other-host"
 if err:=a.RetireWatchdog(context.Background(),"host-compute");err==nil{
  t.Fatal("watchdog with unrelated host label was accepted")
 }
 d.Config.Labels["io.runnerhub.update.host"]="host-compute"
 d.State.Running=false
 if err:=a.RetireWatchdog(context.Background(),"host-compute");err==nil{
  t.Fatal("stopped watchdog identity was accepted")
 }
 d.State.Running=true
 d.Config.Image="ubuntu:latest"
 if err:=a.RetireWatchdog(context.Background(),"host-compute");err==nil{
  t.Fatal("untrusted watchdog image was accepted")
 }
 if d.HostConfig.RestartPolicy.Name!="unless-stopped"{
  t.Fatal("failed retirement unexpectedly changed Docker policy")
 }
}
