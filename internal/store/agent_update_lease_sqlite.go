//go:build !localtest

package store

import(
  "context"
  "database/sql"
  "encoding/json"
  "errors"
  "fmt"
  "time"
  "github.com/eutelic/runnerhub/internal/agentupdate"
  "github.com/eutelic/runnerhub/internal/core"
)

func safeAgentUpdateTerminal(state string)bool{
  // Unknown, review and timed-out swaps block retries across restarts.
  switch state{case "succeeded","rolled-back","aborted-before-swap":return true;default:return false}
}

func(s *SQLite)migrateAgentUpdateLeases(ctx context.Context)error{
  _,err:=s.db.ExecContext(ctx,`CREATE TABLE IF NOT EXISTS agent_update_leases (
    host_id TEXT PRIMARY KEY, operation_id TEXT NOT NULL,
    data BLOB NOT NULL, state TEXT NOT NULL, updated_at TEXT NOT NULL
  )`)
  return err
}

// BeginAgentUpdateLease atomically records the per-host operation and sets
// that exact, existing Docker host to draining. Reuse of the same operation
// ID is idempotent. Another active/unknown operation is never overwritten.
// This is not called by the API until a verified detached executor exists.
func(s *SQLite)BeginAgentUpdateLease(ctx context.Context,l AgentUpdateLease)(AgentUpdateLease,bool,error){
  if l.HostID==""||l.OperationID==""{return AgentUpdateLease{},false,errors.New("exact host and operation IDs required")}
  if err:=(agentupdate.Target{Image:l.TargetImage,Revision:l.TargetRevision}).Validate();err!=nil{return AgentUpdateLease{},false,err}
  tx,err:=s.db.BeginTx(ctx,nil)
  if err!=nil{return AgentUpdateLease{},false,err}
  defer tx.Rollback()
  var priorBytes []byte
  err=tx.QueryRowContext(ctx,`SELECT data FROM agent_update_leases WHERE host_id=?`,l.HostID).Scan(&priorBytes)
  if err!=nil&&!errors.Is(err,sql.ErrNoRows){return AgentUpdateLease{},false,err}
  if err==nil{
    var prior AgentUpdateLease
    if err:=json.Unmarshal(priorBytes,&prior);err!=nil{return AgentUpdateLease{},false,err}
    if prior.OperationID==l.OperationID{return prior,true,nil}
    if !safeAgentUpdateTerminal(prior.State){return prior,false,ErrAgentUpdateBusy}
  }
  var hostBytes []byte
  if err:=tx.QueryRowContext(ctx,`SELECT data FROM hosts WHERE id=?`,l.HostID).Scan(&hostBytes);err!=nil{
    if errors.Is(err,sql.ErrNoRows){return AgentUpdateLease{},false,ErrNotFound}
    return AgentUpdateLease{},false,err
  }
  var host core.Host
  if err:=json.Unmarshal(hostBytes,&host);err!=nil{return AgentUpdateLease{},false,err}
  if host.Provider!="docker"||host.State!=core.HostOnline{
    return AgentUpdateLease{},false,fmt.Errorf("%w: only online Docker hosts can be drained",ErrAgentUpdateBusy)
  }
  host.State=core.HostDraining
  hostBytes,err=json.Marshal(host)
  if err!=nil{return AgentUpdateLease{},false,err}
  now:=time.Now().UTC()
  result,err:=tx.ExecContext(ctx,`UPDATE hosts SET data=?,updated_at=? WHERE id=? AND json_extract(data,'$.state')='online'`,
    hostBytes,now.Format(time.RFC3339Nano),l.HostID)
  if err!=nil{return AgentUpdateLease{},false,err}
  affected,err:=result.RowsAffected()
  if err!=nil{return AgentUpdateLease{},false,err}
  if affected!=1{return AgentUpdateLease{},false,ErrAgentUpdateBusy}
  l.PreviousRevision=host.AgentVersion;l.StartedAt=now;l.State="draining";l.Step="waiting-for-workers";l.UpdatedAt=now
  b,err:=json.Marshal(l)
  if err!=nil{return AgentUpdateLease{},false,err}
  _,err=tx.ExecContext(ctx,`INSERT INTO agent_update_leases(host_id,operation_id,data,state,updated_at)
    VALUES(?,?,?,?,?) ON CONFLICT(host_id) DO UPDATE SET
    operation_id=excluded.operation_id,data=excluded.data,state=excluded.state,updated_at=excluded.updated_at`,
    l.HostID,l.OperationID,b,l.State,now.Format(time.RFC3339Nano))
  if err!=nil{return AgentUpdateLease{},false,err}
  if err:=tx.Commit();err!=nil{return AgentUpdateLease{},false,err}
  return l,false,nil
}

func(s *SQLite)GetAgentUpdateLease(ctx context.Context,hostID string)(AgentUpdateLease,error){
  var b []byte
  err:=s.db.QueryRowContext(ctx,`SELECT data FROM agent_update_leases WHERE host_id=?`,hostID).Scan(&b)
  if errors.Is(err,sql.ErrNoRows){return AgentUpdateLease{},ErrNotFound}
  if err!=nil{return AgentUpdateLease{},err}
  var l AgentUpdateLease
  err=json.Unmarshal(b,&l)
  return l,err
}

// Active update operations survive coordinator restarts. Their states are
// recovered conservatively; unknown/review records are intentionally omitted
// from automatic retry and remain blocked for an administrator.
func(s *SQLite)ListActiveAgentUpdateLeases(ctx context.Context)([]AgentUpdateLease,error){
 rows,err:=s.db.QueryContext(ctx,`SELECT data FROM agent_update_leases
  WHERE state IN ('draining','dispatching','replacing') ORDER BY updated_at,host_id`)
 if err!=nil{return nil,err}
 defer rows.Close()
 out:=[]AgentUpdateLease{}
 for rows.Next(){
  var raw []byte
  if err:=rows.Scan(&raw);err!=nil{return nil,err}
  var l AgentUpdateLease
  if err:=json.Unmarshal(raw,&l);err!=nil{return nil,err}
  out=append(out,l)
 }
 return out,rows.Err()
}

// Transitions use compare-and-swap. They intentionally NEVER resume
// scheduling; fresh same-host-ID heartbeat and cleanup verification are
// prerequisites for a separate terminal-state/resume operation.
func(s *SQLite)TransitionAgentUpdateLease(ctx context.Context,hostID,opID,from,to,step string)error{
  if hostID==""||opID==""||from==""||to==""||step==""{return errors.New("complete lease transition required")}
  tx,err:=s.db.BeginTx(ctx,nil)
  if err!=nil{return err}
  defer tx.Rollback()
  var b []byte
  if err:=tx.QueryRowContext(ctx,`SELECT data FROM agent_update_leases
    WHERE host_id=? AND operation_id=? AND state=?`,hostID,opID,from).Scan(&b);err!=nil{
    if errors.Is(err,sql.ErrNoRows){return ErrAgentUpdateChanged}
    return err
  }
  var l AgentUpdateLease
  if err:=json.Unmarshal(b,&l);err!=nil{return err}
  l.State=to;l.Step=step;l.UpdatedAt=time.Now().UTC()
  b,err=json.Marshal(l)
  if err!=nil{return err}
  result,err:=tx.ExecContext(ctx,`UPDATE agent_update_leases SET data=?,state=?,updated_at=?
    WHERE host_id=? AND operation_id=? AND state=?`,
    b,to,l.UpdatedAt.Format(time.RFC3339Nano),hostID,opID,from)
  if err!=nil{return err}
  affected,err:=result.RowsAffected()
  if err!=nil{return err}
  if affected!=1{return ErrAgentUpdateChanged}
  return tx.Commit()
}
