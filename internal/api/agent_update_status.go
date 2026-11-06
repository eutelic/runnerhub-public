package api

import(
 "errors"
 "net/http"
 "strings"
 "time"

 "github.com/eutelic/runnerhub/internal/agentupdategate"
 "github.com/eutelic/runnerhub/internal/store"
)

// AgentUpdateStatus is read by a dedicated helper using the *existing*
// per-host credential. It reveals no Docker topology or authentication data.
// A result is meaningful only when the exact host + operation are locked.
type AgentUpdateStatus struct{
 HostID string `json:"hostId"`
 OperationID string `json:"operationId"`
 OperationState string `json:"operationState"`
 TargetRevision string `json:"targetRevision"`
 TargetImage string `json:"targetImage"`
 ObservedRevision string `json:"observedRevision"`
 HeartbeatAt time.Time `json:"heartbeatAt"`
 CanReplace bool `json:"canReplace"`
}

// GET /api/v1/agent/update-status?host_id=...&operation_id=...
// Not an admin session endpoint: each host token authorizes only its own ID.
// All future mutations must revalidate state and the durable lease.
func(s *Service) agentUpdateStatus(w http.ResponseWriter,r *http.Request){
 hostID:=strings.TrimSpace(r.URL.Query().Get("host_id"))
 opID:=strings.TrimSpace(r.URL.Query().Get("operation_id"))
 if hostID==""||opID==""||s.Hub==nil||!s.Hub.Authenticate(hostID,bearer(r)){
  unauthorizedAgent(w);return
 }
 st,ok:=s.Store.(store.AgentUpdateLeaseStore)
 if !ok{problem(w,http.StatusServiceUnavailable,errors.New("host update operation persistence unavailable"));return}
 lease,err:=st.GetAgentUpdateLease(r.Context(),hostID)
 if err!=nil{
  if errors.Is(err,store.ErrNotFound){problem(w,http.StatusNotFound,errors.New("no update operation for this host"))}else{problem(w,http.StatusInternalServerError,errors.New("update status lookup failed"))}
  return
 }
 if lease.HostID!=hostID||lease.OperationID!=opID{
  problem(w,http.StatusNotFound,errors.New("no matching update operation"));return
 }
 host,err:=s.Store.GetHost(r.Context(),hostID)
 if err!=nil{problem(w,http.StatusServiceUnavailable,errors.New("host state unavailable"));return}
 response:=AgentUpdateStatus{
  HostID:hostID,OperationID:opID,OperationState:lease.State,
  TargetRevision:lease.TargetRevision,TargetImage:lease.TargetImage,ObservedRevision:host.AgentVersion,
  HeartbeatAt:host.LastSeenAt,
 }
 if lease.State=="replacing"{
  response.CanReplace=agentupdategate.Gate{Store:s.Store}.CheckDrained(r.Context(),hostID)==nil
 }
 writeJSON(w,http.StatusOK,response)
}
