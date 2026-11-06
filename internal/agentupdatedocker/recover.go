package agentupdatedocker

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "strings"

 "github.com/eutelic/runnerhub/internal/agentupdate"
)


// CheckOriginalUpdaterStopped verifies that the exact detached normal updater
// has stopped before a recovery helper may touch the old or new agent.
// A missing helper, Docker daemon error or identity mismatch fails closed;
// never infer safety from an arbitrary 'docker inspect' failure.
func(a *Adapter)CheckOriginalUpdaterStopped(ctx context.Context,hostID string)error{
 if hostID==""||!safeOperationID.MatchString(a.OperationID){
  return errors.New("exact updater host and operation required")
 }
 name:="runnerhub-agent-updater-"+a.OperationID
 out,err:=a.client().Run(ctx,"inspect",name)
 if err!=nil{return errors.New("original updater cannot be verified as stopped")}
 var list []helperInspect
 if err:=json.Unmarshal(out,&list);err!=nil||len(list)!=1{
  return errors.New("ambiguous original updater Docker identity")
 }
 found:=list[0]
 if found.ID==""||!exactContainerID.MatchString(found.ID)||
  found.State.Running||
  !updaterImagePattern.MatchString(found.Config.Image)||
  found.Config.Labels["io.runnerhub.update.host"]!=hostID||
  found.Config.Labels["io.runnerhub.update.operation"]!=a.OperationID||
  found.Config.Labels["io.runnerhub.update.role"]!="updater"{
  return errors.New("original updater has not exited safely with matching operation identity")
 }
 return nil
}

// RecoverFromJournal is an out-of-band recovery primitive, not an automatic
// retry of the upgrade. Its caller must first authenticate the exact host and
// verify that the server's persisted lease is unresolved (never succeeded).
// It restores only IDs recorded before the original disruptive swap.
func(a *Adapter)RecoverFromJournal(ctx context.Context,hostID string,target agentupdate.Target)(RecoveryRecord,error){
 if hostID==""||a.OperationID==""{return RecoveryRecord{},errors.New("recovery needs exact host and operation IDs")}
 rec,err:=a.journal().Load(a.OperationID)
 if err!=nil{return RecoveryRecord{},err}
 if rec.HostID!=hostID{return RecoveryRecord{},errors.New("recovery journal host ID mismatch")}
 if err:=target.Validate();err!=nil{return RecoveryRecord{},err}
 if rec.TargetImage!=target.Image||rec.TargetRevision!=target.Revision{
  return RecoveryRecord{},errors.New("recovery journal release does not match authorized target")
 }
 if rec.State=="succeeded"{
  return RecoveryRecord{},errors.New("cannot roll back a completed update without a separate authorized operation")
 }
 // Even a journal marked rolled-back is only a claim about the previous
 // attempt. After a daemon restart the original container may be stopped
 // again, so always verify its current Docker identity and running state
 // before reporting a confirmed rollback to the control plane.
 // An active normal helper can otherwise race the emergency rollback and
 // mutate the candidate even after the control plane fences its result.
 if err:=a.CheckOriginalUpdaterStopped(ctx,hostID);err!=nil{
  return RecoveryRecord{},err
 }
 credPath:=a.CredentialFile
 // #nosec G101 -- this constant is a local filesystem path, not a hardcoded credential.
 if credPath==""{credPath="/var/lib/runnerhub/agent-credentials.json"}
 credentials,err:=os.ReadFile(credPath)
 if err!=nil{return RecoveryRecord{},errors.New("persisted host identity is unavailable")}
 var cred storedCredential
 if json.Unmarshal(credentials,&cred)!=nil||cred.HostID!=hostID{
  return RecoveryRecord{},errors.New("persisted host identity does not match journal")
 }
 original,err:=a.inspect(ctx,rec.OriginalContainerID)
 if err!=nil{return RecoveryRecord{},errors.New("original agent Docker ID cannot be inspected")}
 if original.ID!=rec.OriginalContainerID||original.ImageID!=rec.OriginalImageID{
  return RecoveryRecord{},errors.New("original agent container identity changed")
 }
 backupName:="/runnerhub-agent-backup-"+rec.OriginalContainerID[:12]
 if original.Name!="/"+agentName&&original.Name!=backupName{
  return RecoveryRecord{},errors.New("original agent has unexpected container name")
 }
 if rec.State=="rolled-back"{
  if !original.State.Running||original.Name!="/"+agentName||
   original.HostConfig.RestartPolicy.Name!="unless-stopped"{
   return RecoveryRecord{},errors.New("previously rolled-back agent is not running with its original name and restart policy")
  }
  return rec,nil
 }
 if original.State.Running&&original.Name==backupName{
  return RecoveryRecord{},errors.New("backup agent is already running under an unexpected name")
 }
 next,err:=a.inspect(ctx,rec.ReplacementContainerID)
 if err!=nil{
  // A crashed rollback may already have removed the failed replacement
  // but not yet restarted the original agent. Only resume that narrow
  // journaled state, and only after an independent *successful* Docker
  // inventory proves the exact candidate ID is absent. Inspect failure
  // alone is not proof that Docker is available or the candidate is gone.
  if rec.State!="rolling-back"{
   return RecoveryRecord{},errors.New("replacement Docker ID missing; manual recovery required")
  }
  present,checkErr:=a.client().Run(ctx,"ps","-a","--no-trunc",
   "--filter","id="+rec.ReplacementContainerID,"--format","{{.ID}}")
  if checkErr!=nil||strings.TrimSpace(string(present))!=""{
   return RecoveryRecord{},errors.New("replacement absence could not be verified; manual recovery required")
  }
  // The original ID, image and persisted host credential were verified
  // above. Docker name collision or failed mutation leaves the lease in
  // recovering rather than guessing which container should be changed.
  if original.Name==backupName{
   if _,err:=a.client().Run(ctx,"rename",original.ID,agentName);err!=nil{
    return RecoveryRecord{},errors.New("original agent name could not be recovered")
   }
  }
  if _,err:=a.client().Run(ctx,"update","--restart=unless-stopped",original.ID);err!=nil{
   return RecoveryRecord{},errors.New("original agent restart policy could not be restored")
  }
  if !original.State.Running{
   if _,err:=a.client().Run(ctx,"start",original.ID);err!=nil{
    return RecoveryRecord{},errors.New("original agent could not be restarted")
   }
  }
  restored,err:=a.inspect(ctx,original.ID)
  if err!=nil||restored.ID!=rec.OriginalContainerID||
   !restored.State.Running||restored.Name!="/"+agentName||
   restored.ImageID!=rec.OriginalImageID||
   restored.HostConfig.RestartPolicy.Name!="unless-stopped"{
   return RecoveryRecord{},errors.New("original agent restoration was not verified")
  }
  if err:=a.writeRecoveryState("rolled-back");err!=nil{return RecoveryRecord{},err}
  return a.journal().Load(rec.OperationID)
 }
 if next.ID!=rec.ReplacementContainerID||next.Config.Image!=rec.TargetImage||
  (next.Name!="/"+agentName&&!strings.HasPrefix(next.Name,"/runnerhub-agent-stage-")){
  return RecoveryRecord{},errors.New("journal replacement container identity changed")
 }
 // Rebuild only the exact trusted state from a persisted journal; never
 // rediscover a replacement by a fuzzy Docker name or image tag.
 a.mu.Lock()
 a.captured=map[string]dockerInspect{original.ID:original}
 a.stages=map[string]dockerInspect{next.ID:next}
 a.mu.Unlock()
 prior:=agentupdate.Snapshot{
  HostID:hostID,ContainerID:original.ID,Image:original.ImageID,
  Running:true,SafeTopology:true,
 }
 stage:=agentupdate.Stage{ContainerID:next.ID}
 if err:=a.Restore(ctx,prior,stage);err!=nil{
  return RecoveryRecord{},fmt.Errorf("journal-scoped rollback not verified: %w",err)
 }
 recovered,err:=a.journal().Load(rec.OperationID)
 if err!=nil{return RecoveryRecord{},err}
 if recovered.State!="rolled-back"{return RecoveryRecord{},errors.New("rollback journal was not finalized")}
 return recovered,nil
}
