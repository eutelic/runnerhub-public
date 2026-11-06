package api

import (
 "context"
 "errors"
 "net/http"
 "strings"
 "time"

 "github.com/eutelic/runnerhub/internal/auth"
 "github.com/eutelic/runnerhub/internal/core"
 "github.com/eutelic/runnerhub/internal/scheduler"
 "github.com/eutelic/runnerhub/internal/store"
)

// maintenanceState reads the durable admission gate. Production SQLite supports
// this optional extension; lean fake stores used in tests may not.
func (s *Service) maintenanceState(ctx context.Context) (store.MaintenanceState,error) {
 if st,ok:=s.Store.(store.MaintenanceStore);ok{return st.GetMaintenance(ctx)}
 return store.MaintenanceState{},nil
}

func (s *Service) requireAdmissionOpen(ctx context.Context) error {
 state,err:=s.maintenanceState(ctx)
 if err!=nil{return err}
 if state.Paused{return errors.New("RunnerHub self-redeployment maintenance: new workers are paused")}
 return nil
}

func (s *Service) maintenanceSnapshot(ctx context.Context) (map[string]any,error) {
 state,err:=s.maintenanceState(ctx)
 if err!=nil{return nil,err}
 var workers []core.Worker
 if st,ok:=s.Store.(store.NonDeletedWorkerReader);ok{
  workers,err=st.ListNonDeletedWorkers(ctx)
 } else {
  workers,err=s.Store.ListWorkers(ctx)
 }
 if err!=nil{return nil,err}
 active:=0
 for _,w:=range workers {
  if scheduler.CountsTowardPoolConcurrency(w.State)||scheduler.OccupiesHostResources(w){active++}
 }
 phase:="running"
 if state.Paused {
  phase="ready_to_redeploy"
  if active>0{phase="draining"}
 }
 return map[string]any{
  "paused":state.Paused,"generation":state.Generation,
  "reason":state.Reason,"requestedBy":state.RequestedBy,
  "requestedAt":state.RequestedAt,"activeWorkers":active,"phase":phase,
 },nil
}

func (s *Service) getMaintenance(w http.ResponseWriter,r *http.Request) {
 if _,ok:=s.Store.(store.MaintenanceStore);!ok{
  problem(w,http.StatusServiceUnavailable,errors.New("maintenance persistence is unavailable"));return
 }
 out,err:=s.maintenanceSnapshot(r.Context())
 if err!=nil{problem(w,500,err);return}
 writeJSON(w,200,out)
}

type resumeMaintenanceRequest struct{ Generation string `json:"generation"` }

func (s *Service) pauseMaintenance(w http.ResponseWriter,r *http.Request) {
 if auth.PrincipalFrom(r.Context()).Role<auth.RoleAdmin{
  problem(w,403,errors.New("admin access required to pause RunnerHub admission"));return
 }
 st,ok:=s.Store.(store.MaintenanceStore)
 if !ok{problem(w,503,errors.New("maintenance persistence is unavailable"));return}
 s.workerScheduleMu.Lock()
 defer s.workerScheduleMu.Unlock()
 current,err:=st.GetMaintenance(r.Context())
 if err!=nil{problem(w,500,err);return}
 if current.Paused{problem(w,409,errors.New("RunnerHub maintenance already active; inspect current generation"));return}
 current=store.MaintenanceState{
  Paused:true,Generation:"mnt-"+randomID(),
  Reason:"runnerhub self-redeployment",
  RequestedBy:actor(r.Context()),RequestedAt:time.Now().UTC(),
 }
 if err:=st.SetMaintenance(r.Context(),current);err!=nil{problem(w,500,err);return}
 _=s.Store.AppendAudit(r.Context(),core.AuditEvent{Actor:actor(r.Context()),Action:"pause_runner_admission",
  ResourceType:"maintenance",ResourceID:current.Generation,Result:"ok",
  SafeDetail:"self-redeployment admission paused; existing workers continue"})
 out,err:=s.maintenanceSnapshot(r.Context())
 if err!=nil{problem(w,500,err);return}
 writeJSON(w,202,out)
}

func (s *Service) resumeMaintenance(w http.ResponseWriter,r *http.Request) {
 if auth.PrincipalFrom(r.Context()).Role<auth.RoleAdmin{
  problem(w,403,errors.New("admin access required to resume RunnerHub admission"));return
 }
 st,ok:=s.Store.(store.MaintenanceStore)
 if !ok{problem(w,503,errors.New("maintenance persistence is unavailable"));return}
 var req resumeMaintenanceRequest
 if !decode(w,r,&req){return}
 req.Generation=strings.TrimSpace(req.Generation)
 s.workerScheduleMu.Lock()
 defer s.workerScheduleMu.Unlock()
 state,err:=st.GetMaintenance(r.Context())
 if err!=nil{problem(w,500,err);return}
 if !state.Paused||state.Generation==""||req.Generation!=state.Generation{
  problem(w,409,errors.New("maintenance generation mismatch or admission is not paused"));return
 }
 state.Paused=false
 if err:=st.SetMaintenance(r.Context(),state);err!=nil{problem(w,500,err);return}
 _=s.Store.AppendAudit(r.Context(),core.AuditEvent{Actor:actor(r.Context()),Action:"resume_runner_admission",
  ResourceType:"maintenance",ResourceID:state.Generation,Result:"ok",
  SafeDetail:"self-redeployment maintenance complete; admissions resumed"})
 out,err:=s.maintenanceSnapshot(r.Context())
 if err!=nil{problem(w,500,err);return}
 writeJSON(w,200,out)
}
