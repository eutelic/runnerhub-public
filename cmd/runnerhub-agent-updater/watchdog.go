package main

import(
 "context"
 "errors"
 "time"

 "github.com/eutelic/runnerhub/internal/agentupdate"
 "github.com/eutelic/runnerhub/internal/agentupdatebridge"
 "github.com/eutelic/runnerhub/internal/agentupdatedocker"
)

// The independent watchdog runs in its own Docker container with a restart
// policy. It never modifies a host during a control-plane outage or while
// the original detached update helper is still running.
type watchdogBridge interface{
 Status(context.Context)(agentupdatebridge.Status,error)
 ClaimRecovery(context.Context,string)error
 Report(context.Context,string)error
}
type watchdogHost interface{
 CheckOriginalUpdaterStopped(context.Context,string)error
 WatchdogJournal()(agentupdatedocker.RecoveryRecord,error)
 RecoverFromJournal(context.Context,string,agentupdate.Target)(agentupdatedocker.RecoveryRecord,error)
 RetireWatchdog(context.Context,string)error
}
type watchdogOptions struct{
 PollEvery time.Duration
 RecoveryGrace time.Duration
 Now func()time.Time
}
func runWatchdog(ctx context.Context,bridge watchdogBridge,host watchdogHost,
 hostID string,target agentupdate.Target,opts watchdogOptions)error{
 if bridge==nil||host==nil||hostID==""{return errors.New("watchdog identity and adapters required")}
 if err:=target.Validate();err!=nil{return err}
 interval:=opts.PollEvery
 if interval<=0{interval=5*time.Second}
 grace:=opts.RecoveryGrace
 if grace<=0{grace=30*time.Second}
 now:=opts.Now
 if now==nil{now=time.Now}
 ticker:=time.NewTicker(interval)
 defer ticker.Stop()
 for{
  status,err:=bridge.Status(ctx)
  if err==nil&&status.HostID==hostID&&status.TargetImage==target.Image&&status.TargetRevision==target.Revision{
   switch status.OperationState{
   case "succeeded","rolled-back","review":
    // Disable our own 'unless-stopped' restart policy before returning,
    // otherwise Docker could restart an already completed monitor forever.
    if err:=host.RetireWatchdog(ctx,hostID);err==nil{return nil}
   case "replacing","recovering":
    // Do not acquire a recovery lease until there is durable evidence of
    // an interrupted update and the exact normal helper has exited.
    if err:=host.CheckOriginalUpdaterStopped(ctx,hostID);err==nil{
     rec,readErr:=host.WatchdogJournal()
     if readErr==nil&&rec.HostID==hostID&&rec.TargetImage==target.Image&&
      rec.TargetRevision==target.Revision&&!rec.UpdatedAt.IsZero()&&
      now().Sub(rec.UpdatedAt)>=grace&&watchdogRecoverableState(rec.State){
      if err:=bridge.ClaimRecovery(ctx,hostID);err==nil{
       if recovered,err:=host.RecoverFromJournal(ctx,hostID,target);err==nil&&recovered.State=="rolled-back"{
        // If the server is offline, keep the durable recovery claim and
        // retry reporting; never resume scheduling locally.
        _=bridge.Report(ctx,"rolled-back")
       }
      }
     }
    }
   }
  }
  select{
  case<-ctx.Done():return ctx.Err()
  case<-ticker.C:
  }
 }
}
func watchdogRecoverableState(state string)bool{
 switch state{
 case "staged","replacing","rolling-back","rolled-back","awaiting-completion":return true
 default:return false
 }
}
