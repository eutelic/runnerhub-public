//go:build !localtest

package store

import (
 "context"
 "database/sql"
 "encoding/json"
 "errors"
 "fmt"
 "time"

 "github.com/eutelic/runnerhub/internal/core"
)

// CompleteAgentUpdateLease accepts only the matching detached helper's
// terminal report. A claimed success is insufficient: the control plane
// must independently observe the exact new revision, a fresh same-ID
// heartbeat, zero unfinished jobs, and the persisted draining host state.
// Resume and terminal lease update are one SQLite transaction.
func(s *SQLite)CompleteAgentUpdateLease(ctx context.Context,hostID,operationID,outcome string,recoveryOwner ...string)(AgentUpdateLease,error){
 if hostID==""||operationID==""{return AgentUpdateLease{},errors.New("exact update host and operation required")}
 if outcome!="succeeded"&&outcome!="rolled-back"&&outcome!="review"{
  return AgentUpdateLease{},errors.New("unsupported updater outcome")
 }
 tx,err:=s.db.BeginTx(ctx,nil)
 if err!=nil{return AgentUpdateLease{},err}
 defer tx.Rollback()
 var b []byte
 err=tx.QueryRowContext(ctx,`SELECT data FROM agent_update_leases WHERE host_id=?`,hostID).Scan(&b)
 if errors.Is(err,sql.ErrNoRows){return AgentUpdateLease{},ErrNotFound}
 if err!=nil{return AgentUpdateLease{},err}
 var l AgentUpdateLease
 if err:=json.Unmarshal(b,&l);err!=nil{return AgentUpdateLease{},err}
 if l.OperationID!=operationID{return AgentUpdateLease{},ErrAgentUpdateChanged}
 if l.State==outcome{return l,nil}
 if l.State!="replacing"&&l.State!="recovering"{return AgentUpdateLease{},ErrAgentUpdateChanged}
 if l.State=="recovering"{
  if outcome!="rolled-back"||len(recoveryOwner)!=1||recoveryOwner[0]==""||
   recoveryOwner[0]!=l.RecoveryOwner{
   return AgentUpdateLease{},fmt.Errorf("%w: recovery is reserved for another helper",ErrAgentUpdateChanged)
  }
 }
 var hostBlob []byte
 err=tx.QueryRowContext(ctx,`SELECT data FROM hosts WHERE id=?`,hostID).Scan(&hostBlob)
 if err!=nil{return AgentUpdateLease{},err}
 var host core.Host
 if err:=json.Unmarshal(hostBlob,&host);err!=nil{return AgentUpdateLease{},err}
 if host.State!=core.HostDraining||host.Provider!="docker"{
  return AgentUpdateLease{},fmt.Errorf("%w: host must remain draining until update verifies",ErrAgentUpdateChanged)
 }
 if outcome=="succeeded"{
  now:=time.Now().UTC()
  if host.AgentVersion!=l.TargetRevision||host.LastSeenAt.IsZero()||
   !host.LastSeenAt.After(l.StartedAt)||now.Sub(host.LastSeenAt)>45*time.Second||
   host.LastSeenAt.After(now.Add(15*time.Second)){
   return AgentUpdateLease{},fmt.Errorf("%w: fresh heartbeat at target revision is not verified",ErrAgentUpdateChanged)
  }
  // Fail closed for any worker that may still own a Docker resource or
  // provider cleanup. Historical terminal workers without a provider
  // handle are not included.
  var pending int
  err=tx.QueryRowContext(ctx,`SELECT COUNT(*) FROM workers
   WHERE json_extract(data,'$.hostId')=?
     AND (
       COALESCE(json_extract(data,'$.state'),'') NOT IN ('deleted','completed','failed')
       OR (
         COALESCE(json_extract(data,'$.state'),'') IN ('completed','failed')
         AND COALESCE(json_extract(data,'$.providerHandle'),'') != ''
       )
     )`,hostID).Scan(&pending)
  if err!=nil{return AgentUpdateLease{},err}
  if pending>0{return AgentUpdateLease{},fmt.Errorf("%w: %d pending worker resources",ErrAgentUpdateChanged,pending)}
  host.State=core.HostOnline
  hb,err:=json.Marshal(host)
  if err!=nil{return AgentUpdateLease{},err}
  _,err=tx.ExecContext(ctx,`UPDATE hosts SET data=?,updated_at=? WHERE id=? AND json_extract(data,'$.state')='draining'`,
   hb,now.Format(time.RFC3339Nano),hostID)
  if err!=nil{return AgentUpdateLease{},err}
 }
 now:=time.Now().UTC()
 fromState:=l.State
 l.State=outcome
 switch outcome{
 case "succeeded":l.Step="verified-and-resumed"
 case "rolled-back":l.Step="old-agent-restored-awaiting-review"
 case "review":l.Step="manual-recovery-required"
 }
 l.UpdatedAt=now
 lb,err:=json.Marshal(l)
 if err!=nil{return AgentUpdateLease{},err}
 changed,err:=tx.ExecContext(ctx,`UPDATE agent_update_leases SET data=?,state=?,updated_at=?
  WHERE host_id=? AND operation_id=? AND state=?`,
  lb,l.State,now.Format(time.RFC3339Nano),hostID,operationID,fromState)
 if err!=nil{return AgentUpdateLease{},err}
 affected,err:=changed.RowsAffected()
 if err!=nil{return AgentUpdateLease{},err}
 if affected!=1{return AgentUpdateLease{},ErrAgentUpdateChanged}
 if err:=tx.Commit();err!=nil{return AgentUpdateLease{},err}
 return l,nil
}
