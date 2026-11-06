package main

import(
 "context"
 "errors"
 "strings"
 "testing"
 "time"

 "github.com/eutelic/runnerhub/internal/agentupdate"
 "github.com/eutelic/runnerhub/internal/agentupdatebridge"
 "github.com/eutelic/runnerhub/internal/agentupdatedocker"
)

type fakeWatchBridge struct{
 status agentupdatebridge.Status
 unavailable bool
 claimed int
 reports int
}
func(b *fakeWatchBridge)Status(context.Context)(agentupdatebridge.Status,error){
 if b.unavailable{return agentupdatebridge.Status{},errors.New("control plane offline")}
 return b.status,nil
}
func(b *fakeWatchBridge)ClaimRecovery(_ context.Context,_ string)error{
 b.claimed++;b.status.OperationState="recovering";return nil
}
func(b *fakeWatchBridge)Report(_ context.Context,what string)error{
 if what!="rolled-back"{return errors.New("watchdog may only report confirmed rollback")}
 b.reports++;b.status.OperationState="rolled-back";return nil
}
type fakeWatchHost struct{
 stopped bool
 journal agentupdatedocker.RecoveryRecord
 retired int
 restored int
}
func(h *fakeWatchHost)CheckOriginalUpdaterStopped(context.Context,string)error{
 if !h.stopped{return errors.New("old updater is still running")}
 return nil
}
func(h *fakeWatchHost)WatchdogJournal()(agentupdatedocker.RecoveryRecord,error){
 return h.journal,nil
}
func(h *fakeWatchHost)RecoverFromJournal(_ context.Context,_ string,_ agentupdate.Target)(agentupdatedocker.RecoveryRecord,error){
 h.restored++
 recovered:=h.journal
 recovered.State="rolled-back"
 return recovered,nil
}
func(h *fakeWatchHost)RetireWatchdog(context.Context,string)error{
 h.retired++;return nil
}
func sampleWatchdog()(agentupdate.Target,*fakeWatchBridge,*fakeWatchHost){
 target:=agentupdate.Target{Image:agentupdate.AgentImage+"@sha256:"+strings.Repeat("a",64),
  Revision:strings.Repeat("b",40)}
 b:=&fakeWatchBridge{status:agentupdatebridge.Status{
  HostID:"host-compute",OperationID:"op-123",OperationState:"replacing",
  TargetImage:target.Image,TargetRevision:target.Revision,
 }}
 h:=&fakeWatchHost{stopped:true,journal:agentupdatedocker.RecoveryRecord{
  HostID:"host-compute",OperationID:"op-123",
  TargetImage:target.Image,TargetRevision:target.Revision,State:"replacing",
  UpdatedAt:time.Now().Add(-time.Minute),
 }}
 return target,b,h
}
func TestWatchdogRestoresStoppedFailedUpdaterOnlyAfterServerClaim(t *testing.T){
 target,b,h:=sampleWatchdog()
 ctx,stop:=context.WithTimeout(context.Background(),250*time.Millisecond)
 defer stop()
 err:=runWatchdog(ctx,b,h,"host-compute",target,watchdogOptions{
  PollEvery:time.Millisecond,RecoveryGrace:time.Millisecond,
 })
 if err!=nil{t.Fatal(err)}
 if b.claimed!=1||b.reports!=1||h.restored!=1||h.retired!=1{
  t.Fatalf("watchdog did not claim, restore, report and retire exactly once: claim=%d report=%d restore=%d retire=%d",
   b.claimed,b.reports,h.restored,h.retired)
 }
}
func TestWatchdogDoesNotTouchRunningUpdaterOrOfflineControlPlane(t *testing.T){
 for _,offline:=range []bool{false,true}{
  target,b,h:=sampleWatchdog()
  b.unavailable=offline
  h.stopped=false
  ctx,cancel:=context.WithTimeout(context.Background(),20*time.Millisecond)
  err:=runWatchdog(ctx,b,h,"host-compute",target,watchdogOptions{
   PollEvery:time.Millisecond,RecoveryGrace:time.Millisecond,
  })
  cancel()
  if !errors.Is(err,context.DeadlineExceeded)||b.claimed!=0||h.restored!=0{
   t.Fatalf("unsafe recovery without stopped helper and online server: err=%v claims=%d restores=%d",err,b.claimed,h.restored)
  }
 }
}
func TestWatchdogRetiresAfterTerminalStateWithoutRollback(t *testing.T){
 for _,state:=range []string{"succeeded","rolled-back","review"}{
  target,b,h:=sampleWatchdog()
  b.status.OperationState=state
  err:=runWatchdog(context.Background(),b,h,"host-compute",target,watchdogOptions{
   PollEvery:time.Millisecond,
  })
  if err!=nil||h.retired!=1||b.claimed!=0||h.restored!=0{
   t.Fatalf("terminal watchdog state %s should retire without mutation: err=%v",state,err)
  }
 }
}
func TestWatchdogRejectsMismatchedJournalTarget(t *testing.T){
 target,b,h:=sampleWatchdog()
 h.journal.TargetRevision=strings.Repeat("c",40)
 ctx,cancel:=context.WithTimeout(context.Background(),20*time.Millisecond)
 err:=runWatchdog(ctx,b,h,"host-compute",target,watchdogOptions{
  PollEvery:time.Millisecond,RecoveryGrace:time.Millisecond,
 })
 cancel()
 if !errors.Is(err,context.DeadlineExceeded)||b.claimed!=0||h.restored!=0{
  t.Fatalf("wrong source revision triggered recovery: err=%v claims=%d",err,b.claimed)
 }
}
