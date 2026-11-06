//go:build !localtest

package store

import (
 "context"
 "database/sql"
 "encoding/json"
 "errors"
 "regexp"
 "time"

 "github.com/eutelic/runnerhub/internal/core"
)

// Recovery ownership is an unpredictable per-process 128-bit identifier.
// It fences duplicate helpers, not authentication; the host agent token
// authenticates the request and the update lease scopes it to one host.
var agentUpdateRecoveryOwnerPattern=regexp.MustCompile(`^[a-f0-9]{32}$`)

// ClaimAgentUpdateRecovery commits exclusive ownership before an emergency
// helper may restore an old Docker container. Only the same owner can
// idempotently confirm a claim. An unrelated helper must never be admitted
// simply because the shared operation already has state "recovering".
func(s *SQLite)ClaimAgentUpdateRecovery(ctx context.Context,hostID,opID,owner string)(AgentUpdateLease,error){
 if hostID==""||opID==""||!agentUpdateRecoveryOwnerPattern.MatchString(owner){
  return AgentUpdateLease{},errors.New("exact host, operation and 128-bit recovery owner required")
 }
 tx,err:=s.db.BeginTx(ctx,nil)
 if err!=nil{return AgentUpdateLease{},err}
 defer tx.Rollback()
 var data []byte
 err=tx.QueryRowContext(ctx,`SELECT data FROM agent_update_leases WHERE host_id=? AND operation_id=?`,hostID,opID).Scan(&data)
 if errors.Is(err,sql.ErrNoRows){return AgentUpdateLease{},ErrNotFound}
 if err!=nil{return AgentUpdateLease{},err}
 var l AgentUpdateLease
 if err:=json.Unmarshal(data,&l);err!=nil{return AgentUpdateLease{},err}
 // A host may have been manually re-admitted or acquired resources after
 // the original update stopped. No recovery helper may mutate Docker unless
 // the database still fences scheduling for this exact Docker host.
 var hb []byte
 if err:=tx.QueryRowContext(ctx,`SELECT data FROM hosts WHERE id=?`,hostID).Scan(&hb);err!=nil{
  return AgentUpdateLease{},ErrAgentUpdateChanged
 }
 var host core.Host
 if err:=json.Unmarshal(hb,&host);err!=nil{return AgentUpdateLease{},err}
 if host.State!=core.HostDraining||host.Provider!="docker"{
  return AgentUpdateLease{},ErrAgentUpdateChanged
 }
 var pending int
 if err:=tx.QueryRowContext(ctx,`SELECT COUNT(*) FROM workers
  WHERE json_extract(data,'$.hostId')=?
  AND (COALESCE(json_extract(data,'$.state'),'') NOT IN ('deleted','completed','failed')
   OR (COALESCE(json_extract(data,'$.state'),'') IN ('completed','failed')
   AND COALESCE(json_extract(data,'$.providerHandle'),'') != ''))`,hostID).Scan(&pending);err!=nil{
  return AgentUpdateLease{},err
 }
 if pending>0{return AgentUpdateLease{},ErrAgentUpdateChanged}
 if l.State=="recovering"{
  if l.RecoveryOwner!=owner{return AgentUpdateLease{},ErrAgentUpdateChanged}
  return l,nil
 }
 if l.State!="replacing"{return AgentUpdateLease{},ErrAgentUpdateChanged}
 l.State="recovering"
 l.Step="journal-recovery-claimed"
 l.RecoveryOwner=owner
 l.UpdatedAt=time.Now().UTC()
 raw,err:=json.Marshal(l)
 if err!=nil{return AgentUpdateLease{},err}
 updated,err:=tx.ExecContext(ctx,`UPDATE agent_update_leases SET data=?,state=?,updated_at=?
  WHERE host_id=? AND operation_id=? AND state='replacing'`,
  raw,l.State,l.UpdatedAt.Format(time.RFC3339Nano),hostID,opID)
 if err!=nil{return AgentUpdateLease{},err}
 n,err:=updated.RowsAffected()
 if err!=nil{return AgentUpdateLease{},err}
 if n!=1{return AgentUpdateLease{},ErrAgentUpdateChanged}
 if err:=tx.Commit();err!=nil{return AgentUpdateLease{},err}
 return l,nil
}
