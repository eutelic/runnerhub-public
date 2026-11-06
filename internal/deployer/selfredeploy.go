package deployer

import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net/http"
 "os"
 "path/filepath"
 "strings"
 "sync"
 "time"
)

type maintenanceSnapshot struct {
 Paused bool `json:"paused"`
 Generation string `json:"generation"`
 ActiveWorkers int `json:"activeWorkers"`
 Phase string `json:"phase"`
}

// GitHub workflow clients never receive this trusted maintenance credential.
func (s *Service) maintenanceCall(ctx context.Context,t Target,method,path string,payload any) (maintenanceSnapshot,error) {
 if strings.TrimSpace(s.MaintenanceToken)=="" {return maintenanceSnapshot{},errors.New("RunnerHub maintenance credential is not configured")}
 var body io.Reader
 if payload!=nil {
  data,err:=json.Marshal(payload)
  if err!=nil{return maintenanceSnapshot{},err}
  body=bytes.NewReader(data)
 }
 req,err:=http.NewRequestWithContext(ctx,method,t.MaintenanceURL+path,body)
 if err!=nil{return maintenanceSnapshot{},err}
 req.Header.Set("Authorization","Bearer "+s.MaintenanceToken)
 if payload!=nil{req.Header.Set("Content-Type","application/json")}
 client:=s.HTTPClient
 if client==nil{client=&http.Client{Timeout:10*time.Second}}
 resp,err:=client.Do(req)
 if err!=nil{return maintenanceSnapshot{},fmt.Errorf("contact RunnerHub maintenance control: %w",err)}
 defer resp.Body.Close()
 raw,err:=io.ReadAll(io.LimitReader(resp.Body,4096))
 if err!=nil{return maintenanceSnapshot{},err}
 if resp.StatusCode<200||resp.StatusCode>=300{
  return maintenanceSnapshot{},fmt.Errorf("RunnerHub maintenance endpoint returned HTTP %d",resp.StatusCode)
 }
 var state maintenanceSnapshot
 if err:=json.Unmarshal(raw,&state);err!=nil{return maintenanceSnapshot{},fmt.Errorf("decode maintenance state: %w",err)}
 return state,nil
}

func (s *Service) selfStatusPath(target string) string {
 return filepath.Join(filepath.Dir(s.AuditPath),"self-redeploy-"+target+".json")
}

func (s *Service) writeSelfStatus(rec deploymentRecord) error {
 if err:=s.appendAudit(rec);err!=nil{return err}
 s.auditMu.Lock()
 defer s.auditMu.Unlock()
 path:=s.selfStatusPath(rec.Target)
 if err:=os.MkdirAll(filepath.Dir(path),0700);err!=nil{return err}
 data,err:=json.Marshal(rec)
 if err!=nil{return err}
 f,err:=os.CreateTemp(filepath.Dir(path),".runnerhub-self-redeploy-*")
 if err!=nil{return err}
 tmp:=f.Name()
 defer os.Remove(tmp)
 if err:=f.Chmod(0600);err!=nil{f.Close();return err}
 if _,err:=f.Write(data);err!=nil{f.Close();return err}
 if err:=f.Sync();err!=nil{f.Close();return err}
 if err:=f.Close();err!=nil{return err}
 return os.Rename(tmp,path)
}

func (s *Service) readSelfStatus(target string) (deploymentRecord,error) {
 raw,err:=os.ReadFile(s.selfStatusPath(target))
 if err!=nil{return deploymentRecord{},err}
 var rec deploymentRecord
 if err:=json.Unmarshal(raw,&rec);err!=nil{return deploymentRecord{},err}
 if rec.Target!=target{return deploymentRecord{},errors.New("deployment target does not match stored status")}
 return rec,nil
}

func ongoingSelfDeployment(result string)bool{
 switch result {
 case "queued","pausing","draining","redeploying","verifying","resuming":return true
 default:return false
 }
}

func (s *Service) setSelfRunning(id string,running bool) {
 s.selfMu.Lock()
 defer s.selfMu.Unlock()
 if s.selfActive==nil{s.selfActive=map[string]bool{}}
 if running{s.selfActive[id]=true}else{delete(s.selfActive,id)}
}
func (s *Service) isSelfRunning(id string)bool{
 s.selfMu.Lock()
 defer s.selfMu.Unlock()
 return s.selfActive[id]
}

// Capability probe prevents a newer publishing workflow from asking an older
// synchronous deployer to restart RunnerHub before CI has drained.
func (s *Service) handleDeploymentCapabilities(w http.ResponseWriter,r *http.Request) {
 targetID:=r.PathValue("target")
 target,ok:=s.Policy.Authenticate(targetID,bearerToken(r.Header.Get("Authorization")))
 if !ok{http.Error(w,"unauthorized",http.StatusUnauthorized);return}
 ready:=target.SelfRedeploy&&strings.TrimSpace(s.MaintenanceToken)!=""
 writeJSON(w,200,map[string]any{"async_self_redeploy":ready,"maintenance_gate":ready})
}

func (s *Service) handleDeploymentStatus(w http.ResponseWriter,r *http.Request) {
 targetID:=r.PathValue("target")
 token:=bearerToken(r.Header.Get("Authorization"))
 target,ok:=s.Policy.Authenticate(targetID,token)
 if !ok{http.Error(w,"unauthorized",http.StatusUnauthorized);return}
 if !target.SelfRedeploy{http.Error(w,"status is available only for RunnerHub self-redeployment",http.StatusNotFound);return}
 rec,err:=s.readSelfStatus(targetID)
 if errors.Is(err,os.ErrNotExist){http.NotFound(w,r);return}
 if err!=nil{http.Error(w,"cannot read deployment status",500);return}
 if ongoingSelfDeployment(rec.Result)&&!s.isSelfRunning(targetID){
  rec.Result="recovery_required"
  rec.Detail="Standalone deployer restarted or lost its active operation; verify TrueNAS and RunnerHub maintenance state before recovery"
 }
 writeJSON(w,200,rec)
}

// The standalone deployer survives RunnerHub's restart. The requesting CI
// job finishes after the 202 response, so it cannot deadlock the drain.
func (s *Service) runSelfRedeploy(parent context.Context,t Target,rec deploymentRecord,lock *sync.Mutex) {
 defer lock.Unlock()
 defer s.setSelfRunning(t.ID,false)
 ctx,cancel:=context.WithTimeout(parent,t.DrainTimeout()+t.Timeout()+30*time.Second)
 defer cancel()
 record:=func(state,detail string){
  rec.Result=state
  rec.Detail=bounded(detail,300)
  if !ongoingSelfDeployment(state){rec.CompletedAt=time.Now().UTC()}
  _=s.writeSelfStatus(rec)
 }
 // Check the TrueNAS custom-app configuration and FULL_ADMIN permission
 // before pausing new CI work. An APPS_WRITE-only key cannot edit arbitrary
 // custom Compose and must never strand CI at the end of the drain.
 verifier,ok:=s.TrueNAS.(TrueNASImagePreflight)
 if !ok{
  record("failed","TrueNAS client has no immutable-update capability preflight; CI admissions remain open")
  return
 }
 preflightCtx,preflightCancel:=context.WithTimeout(ctx,20*time.Second)
 preflightErr:=verifier.VerifyImmutableImageUpdate(preflightCtx,t.App,rec.Image)
 preflightCancel()
 if preflightErr!=nil{
  record("failed","TrueNAS custom Compose capability check failed before pausing CI: "+safeDetail(preflightErr))
  return
 }
 record("pausing","Pausing new RunnerHub CI admissions")
 paused,err:=s.maintenanceCall(ctx,t,http.MethodPost,"/api/v1/maintenance/pause",nil)
 if err!=nil{
  // A timeout or lost response does not prove that the pause failed.
  // The control plane may have persisted the admission gate already.
  // Never report an ordinary failure that could strand CI admissions.
  record("recovery_required","Maintenance pause response was ambiguous; inspect the persisted admission state before retrying: "+safeDetail(err))
  return
 }
 if !paused.Paused||paused.Generation==""{
  record("recovery_required","Maintenance pause was not confirmed; inspect the control-plane state")
  return
 }
 generation:=paused.Generation
 resumeBeforeRedeploy:=func(cause error){
  recoverCtx,recoverCancel:=context.WithTimeout(context.WithoutCancel(parent),15*time.Second)
  defer recoverCancel()
  _,resumeErr:=s.maintenanceCall(recoverCtx,t,http.MethodPost,"/api/v1/maintenance/resume",
   map[string]string{"generation":generation})
  if resumeErr!=nil{
   record("recovery_required","Drain failed and could not restore admissions: "+safeDetail(resumeErr))
   return
  }
  record("failed","Drain aborted before deployment: "+safeDetail(cause))
 }
 drainCtx,drainCancel:=context.WithTimeout(ctx,t.DrainTimeout())
 defer drainCancel()
 lastCount:=-1
 for {
  state,checkErr:=s.maintenanceCall(drainCtx,t,http.MethodGet,"/api/v1/maintenance",nil)
  if checkErr!=nil {resumeBeforeRedeploy(checkErr);return}
  if !state.Paused||state.Generation!=generation {
   record("recovery_required","Maintenance generation changed during drain; refusing redeploy")
   return
  }
  if state.ActiveWorkers<0{
   record("recovery_required","Invalid negative active worker count")
   return
  }
  if state.ActiveWorkers!=lastCount{
   record("draining",fmt.Sprintf("Waiting for %d active workers to finish and clean up",state.ActiveWorkers))
   lastCount=state.ActiveWorkers
  }
  if state.ActiveWorkers==0&&state.Phase=="ready_to_redeploy"{break}
  select{
  case <-drainCtx.Done():resumeBeforeRedeploy(drainCtx.Err());return
  case <-time.After(3*time.Second):
  }
 }
 if ctx.Err()!=nil{resumeBeforeRedeploy(ctx.Err());return}
 record("redeploying","All workers drained; redeploying RunnerHub via TrueNAS")
 // Bound the app update separately: its deadline must not inherit the
 // four-hour CI drain budget if TrueNAS stops responding.
 deployCtx,stopDeploy:=context.WithTimeout(ctx,t.Timeout())
	imageRedeployer, ok := s.TrueNAS.(TrueNASImageRedeployer)
	if !ok {
		err = errors.New("TrueNAS client does not support immutable RunnerHub image updates")
	} else {
		err = imageRedeployer.RedeployImageAndWait(deployCtx, t.App, rec.Image)
	}
 stopDeploy()
 if err==nil&&t.HealthURL!=""{
  record("verifying","Waiting for the updated RunnerHub health endpoint")
  healthCtx,stopHealth:=context.WithTimeout(ctx,t.Timeout())
  err=s.waitForHealth(healthCtx,t.HealthURL)
  stopHealth()
 }
 if err!=nil{
  record("recovery_required","TrueNAS redeploy or health verification failed; admission remains paused: "+safeDetail(err))
  return
 }
 record("resuming","RunnerHub is healthy; checking maintenance generation before resuming admissions")
 state,err:=s.maintenanceCall(ctx,t,http.MethodGet,"/api/v1/maintenance",nil)
 if err!=nil||!state.Paused||state.Generation!=generation||state.ActiveWorkers!=0{
  record("recovery_required","Post-redeploy maintenance state is unverified; manual review required")
  return
 }
 _,err=s.maintenanceCall(ctx,t,http.MethodPost,"/api/v1/maintenance/resume",map[string]string{"generation":generation})
 if err!=nil{record("recovery_required","RunnerHub healthy but admissions could not be resumed: "+safeDetail(err));return}
 record("success","RunnerHub redeployed, health verified, and new CI admission resumed")
}
