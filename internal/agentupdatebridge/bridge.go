// Package agentupdatebridge provides a narrowly scoped, authenticated bridge
// from the detached host updater to the control plane. It never stores tokens
// in process arguments or persistently in an update operation.
package agentupdatebridge

import(
 "context"
 "bytes"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net"
 "net/http"
 "net/url"
 "os"
 "regexp"
 "strings"
 "time"
 "github.com/eutelic/runnerhub/internal/agentupdate"
)

type Status struct{
 HostID string `json:"hostId"`
 OperationID string `json:"operationId"`
 OperationState string `json:"operationState"`
 TargetRevision string `json:"targetRevision"`
 TargetImage string `json:"targetImage"`
 ObservedRevision string `json:"observedRevision"`
 HeartbeatAt time.Time `json:"heartbeatAt"`
 CanReplace bool `json:"canReplace"`
}
type Config struct{
 ControlURL string
 HostID string
 OperationID string
 TargetImage string
 TargetRevision string
 CredentialFile string
 RecoveryOwner string
 AllowInsecureHTTP bool
 HTTPClient *http.Client
}
var recoveryOwnerPattern=regexp.MustCompile(`^[0-9a-f]{32}$`)
type Bridge struct{cfg Config;client *http.Client}
func New(cfg Config)(*Bridge,error){
 u,err:=url.Parse(cfg.ControlURL)
 if err!=nil||u.Host==""||u.User!=nil||u.RawQuery!=""||u.Fragment!=""{
  return nil,errors.New("invalid update control-plane URL")
 }
 if u.Scheme!="https"{
  ip:=net.ParseIP(u.Hostname())
  loopback:=strings.EqualFold(u.Hostname(),"localhost")||(ip!=nil&&ip.IsLoopback())
  if u.Scheme!="http"||(!loopback&&!cfg.AllowInsecureHTTP){return nil,errors.New("agent updater requires HTTPS or explicit protected-network HTTP authorization")}
 }
 if cfg.HostID==""||cfg.OperationID==""||cfg.CredentialFile==""{return nil,errors.New("exact update host, operation and credential file required")}
 if cfg.RecoveryOwner!=""&&!recoveryOwnerPattern.MatchString(cfg.RecoveryOwner){
  return nil,errors.New("invalid recovery helper owner")
 }
 if err:=(agentupdate.Target{Image:cfg.TargetImage,Revision:cfg.TargetRevision}).Validate();err!=nil{return nil,err}
 cfg.ControlURL=strings.TrimRight(cfg.ControlURL,"/")
 cli:=cfg.HTTPClient
 if cli==nil{cli=&http.Client{Timeout:15*time.Second}}
 return &Bridge{cfg:cfg,client:cli},nil
}
func(b *Bridge)token() (string,error){
 raw,err:=os.ReadFile(b.cfg.CredentialFile)
 if err!=nil{return "",errors.New("agent credential unavailable")}
 var cred struct{HostID string `json:"hostId"`;Token string `json:"token"`}
 if json.Unmarshal(raw,&cred)!=nil||cred.HostID!=b.cfg.HostID||cred.Token==""{
  return "",errors.New("credential does not match the selected host ID")
 }
 return cred.Token,nil
}
func(b *Bridge)status(ctx context.Context)(Status,error){
 token,err:=b.token();if err!=nil{return Status{},err}
 endpoint:=b.cfg.ControlURL+"/api/v1/agent/update-status?host_id="+url.QueryEscape(b.cfg.HostID)+"&operation_id="+url.QueryEscape(b.cfg.OperationID)
 request,err:=http.NewRequestWithContext(ctx,http.MethodGet,endpoint,nil)
 if err!=nil{return Status{},err}
 request.Header.Set("Authorization","Bearer "+token)
 res,err:=b.client.Do(request)
 if err!=nil{return Status{},err}
 defer res.Body.Close()
 if res.StatusCode!=http.StatusOK{
  return Status{},fmt.Errorf("update status rejected (HTTP %d)",res.StatusCode)
 }
 var status Status
 dec:=json.NewDecoder(io.LimitReader(res.Body,16<<10))
 if err:=dec.Decode(&status);err!=nil{return Status{},err}
 if status.HostID!=b.cfg.HostID||status.OperationID!=b.cfg.OperationID||status.TargetImage!=b.cfg.TargetImage||status.TargetRevision!=b.cfg.TargetRevision{
  return Status{},errors.New("control plane returned mismatched update identity")
 }
 return status,nil
}
// Status returns the authenticated, exact-operation read-only status for
// the detached watchdog. It does not authorize Docker mutation.
func(b *Bridge)Status(ctx context.Context)(Status,error){return b.status(ctx)}
func(b *Bridge)CheckDrained(ctx context.Context,hostID string)error{
 if hostID!=b.cfg.HostID{return errors.New("update guard host ID mismatch")}
 v,err:=b.status(ctx)
 if err!=nil{return err}
 if v.OperationState!="replacing"||!v.CanReplace{
  return errors.New("host update admission is not safely drained")
 }
 return nil
}
// ClaimRecovery atomically moves the exact host operation into the
// server-side recovering state, fencing a late successful replacement report.
// A read-only status check is insufficient; Docker must not be touched until
// this compare-and-swap is durably acknowledged by the control plane.
func(b *Bridge)ClaimRecovery(ctx context.Context,hostID string)error{
 if !recoveryOwnerPattern.MatchString(b.cfg.RecoveryOwner){return errors.New("recovery helper identity not configured")}
 if hostID!=b.cfg.HostID{return errors.New("recovery host identity mismatch")}
 s,err:=b.status(ctx)
 if err!=nil{return err}
 if s.OperationState!="replacing"&&s.OperationState!="recovering"{
  return errors.New("server update operation is not eligible for journal recovery")
 }
 token,err:=b.token()
 if err!=nil{return err}
 body,err:=json.Marshal(struct{
  HostID string `json:"hostId"`
  OperationID string `json:"operationId"`
  RecoveryOwner string `json:"recoveryOwner"`
 }{HostID:b.cfg.HostID,OperationID:b.cfg.OperationID,RecoveryOwner:b.cfg.RecoveryOwner})
 if err!=nil{return err}
 req,err:=http.NewRequestWithContext(ctx,http.MethodPost,
  b.cfg.ControlURL+"/api/v1/agent/update-recovery-start",bytes.NewReader(body))
 if err!=nil{return err}
 req.Header.Set("Authorization","Bearer "+token)
 req.Header.Set("Content-Type","application/json")
 res,err:=b.client.Do(req)
 if err!=nil{return err}
 defer res.Body.Close()
 if res.StatusCode!=http.StatusOK{
  return fmt.Errorf("host rollback recovery claim rejected (HTTP %d)",res.StatusCode)
 }
 var result struct{
  HostID string `json:"hostId"`
  OperationID string `json:"operationId"`
  State string `json:"state"`
  RecoveryOwner string `json:"recoveryOwner"`
 }
 if err:=json.NewDecoder(io.LimitReader(res.Body,16<<10)).Decode(&result);err!=nil{return err}
 if result.HostID!=hostID||result.OperationID!=b.cfg.OperationID||
  result.State!="recovering"||result.RecoveryOwner!=b.cfg.RecoveryOwner{
  return errors.New("server failed to confirm exclusive recovery lease")
 }
 return nil
}
func(b *Bridge)WaitForHeartbeat(ctx context.Context,hostID,revision string,after time.Time)error{
 if hostID!=b.cfg.HostID||revision==""||after.IsZero(){return errors.New("heartbeat target identity or start time invalid")}
 ticker:=time.NewTicker(2*time.Second)
 defer ticker.Stop()
 for{
  s,err:=b.status(ctx)
  if err!=nil{return err}
  if s.TargetRevision!=revision{return errors.New("configured release changed during update")}
  if s.ObservedRevision==revision&&s.HeartbeatAt.After(after){return nil}
  select{case<-ctx.Done():return ctx.Err();case<-ticker.C:}
 }
}

func(b *Bridge)Report(ctx context.Context,outcome string)error{
 if outcome!="succeeded"&&outcome!="rolled-back"&&outcome!="review"{return errors.New("unsupported update result")}
 token,err:=b.token()
 if err!=nil{return err}
 body,err:=json.Marshal(struct{
  HostID string `json:"hostId"`
  OperationID string `json:"operationId"`
  Outcome string `json:"outcome"`
  RecoveryOwner string `json:"recoveryOwner,omitempty"`
 }{HostID:b.cfg.HostID,OperationID:b.cfg.OperationID,Outcome:outcome,RecoveryOwner:b.cfg.RecoveryOwner})
 if err!=nil{return err}
 req,err:=http.NewRequestWithContext(ctx,http.MethodPost,b.cfg.ControlURL+"/api/v1/agent/update-result",bytes.NewReader(body))
 if err!=nil{return err}
 req.Header.Set("Authorization","Bearer "+token)
 req.Header.Set("Content-Type","application/json")
 res,err:=b.client.Do(req)
 if err!=nil{return err}
 defer res.Body.Close()
 if res.StatusCode!=http.StatusOK{return fmt.Errorf("update completion rejected (HTTP %d)",res.StatusCode)}
 return nil
}
