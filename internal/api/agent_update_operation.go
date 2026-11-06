package api

import(
 "errors"
 "net/http"

 "github.com/eutelic/runnerhub/internal/auth"
 "github.com/eutelic/runnerhub/internal/store"
)

// GET /api/v1/hosts/{id}/update-agent returns the persisted, nonsecret
// operation state across dashboard and control-plane process restarts.
func(s *Service)hostAgentUpdateOperation(w http.ResponseWriter,r *http.Request){
 if auth.PrincipalFrom(r.Context()).Role<auth.RoleAdmin{
  problem(w,http.StatusForbidden,errors.New("host agent update operations require admin access"));return
 }
 leases,ok:=s.Store.(store.AgentUpdateLeaseStore)
 if !ok{problem(w,http.StatusServiceUnavailable,errors.New("agent update persistence unavailable"));return}
 l,err:=leases.GetAgentUpdateLease(r.Context(),r.PathValue("id"))
 if err!=nil{
  if errors.Is(err,store.ErrNotFound){problem(w,http.StatusNotFound,errors.New("no update operation for host"))}else{problem(w,http.StatusInternalServerError,errors.New("agent update operation unavailable"))}
  return
 }
 writeJSON(w,http.StatusOK,l)
}
