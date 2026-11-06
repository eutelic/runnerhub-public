package api

import(
 "context"
 "errors"

 "github.com/eutelic/runnerhub/internal/agent"
 "github.com/eutelic/runnerhub/internal/core"
 "github.com/eutelic/runnerhub/internal/store"
)

// The update-command result acknowledges only that a detached, trusted
// helper was launched. It does NOT mean the new agent image is running.
// This transition arms the helper to start the swap; until the result is
// durably processed, /agent/update-status must refuse the replacement.
func(s *Service)handleAgentUpdaterLaunchResult(ctx context.Context,rec store.CommandRecord,hostID string,res agent.CommandResult)error{
 if rec.HostID!=hostID||rec.Command.AgentUpdate==nil||
  rec.Command.WorkerID!=res.WorkerID||rec.Command.AgentUpdate.OperationID!=res.WorkerID||
  rec.Command.AgentUpdate.HostID!=hostID{
  return errors.New("agent update command identity mismatch")
 }
 leases,ok:=s.Store.(store.AgentUpdateLeaseStore)
 if !ok{return errors.New("durable agent update operations unavailable")}
 lease,err:=leases.GetAgentUpdateLease(ctx,hostID)
 if err!=nil{return err}
 if lease.OperationID!=res.WorkerID{
  return errors.New("agent update operation identity mismatch")
 }
 // An agent can retry the same command acknowledgment after the control
 // plane committed the lease transition but crashed before AckCommand.
 // Replays may never arm a *different* operation, but a matching successful
 // launch must be safe to acknowledge after an earlier transition.
 if res.OK && (lease.State=="replacing"||lease.State=="succeeded"||
  lease.State=="rolled-back"||
  (lease.State=="review"&&lease.Step!="helper-launch-failed")){
  return nil
 }
 if !res.OK&&lease.State=="review"&&lease.Step=="helper-launch-failed"{return nil}
 if lease.State!="dispatching"{
  return errors.New("agent update is not awaiting helper launch confirmation")
 }
 nextState,step:="replacing","helper-launched"
 if !res.OK{nextState,step="review","helper-launch-failed"}
 if err:=leases.TransitionAgentUpdateLease(ctx,hostID,res.WorkerID,"dispatching",nextState,step);err!=nil{return err}
 // Never put raw agent/remote error output in persistent audit records.
 result:="accepted"
 if !res.OK{result="blocked"}
 _=s.Store.AppendAudit(ctx,core.AuditEvent{
  Actor:"agent:"+hostID,Action:"agent_update_helper_launch",
  ResourceType:"host",ResourceID:hostID,Result:result,
  SafeDetail:step,
 })
 return nil
}
