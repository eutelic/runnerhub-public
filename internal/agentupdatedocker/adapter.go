// Package agentupdatedocker implements a narrowly scoped Docker adapter for
// RunnerHub's detached agent-replacement transaction. It is NOT invoked from
// the web API or from the live agent process.
package agentupdatedocker

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "os/exec"
 "path/filepath"
 "slices"
 "strings"
 "sync"
 "time"

 "github.com/eutelic/runnerhub/internal/agentupdate"
)

const agentName="runnerhub-agent"
const entrypoint="/usr/local/bin/runnerhub-agent-entrypoint"

// CLI permits fake Docker implementations in tests; no command is invoked
// through a shell. Never propagate raw CLI stderr (which may contain secrets).
type CLI interface{ Run(context.Context,...string)([]byte,error) }
type ExecCLI struct{}
func(ExecCLI)Run(ctx context.Context,args ...string)([]byte,error){
 cmd:=exec.CommandContext(ctx,"docker",args...)
 out,err:=cmd.CombinedOutput()
 if err!=nil{return nil,fmt.Errorf("Docker %s failed: %w",safeAction(args),err)}
 return out,nil
}
func safeAction(args []string)string{
 if len(args)==0{return "command"}
 switch args[0]{case "inspect","pull","create","stop","rename","start","rm","update","image":return args[0]}
 return "operation"
}

type dockerInspect struct{
 ID string `json:"Id"`
 Name string `json:"Name"`
 ImageID string `json:"Image"`
 State struct{
  Running bool `json:"Running"`
  StartedAt time.Time `json:"StartedAt"`
 } `json:"State"`
 Config struct{
  Image string `json:"Image"`
  Env []string `json:"Env"`
  Entrypoint []string `json:"Entrypoint"`
  Cmd []string `json:"Cmd"`
  Labels map[string]string `json:"Labels"`
 } `json:"Config"`
 HostConfig struct{
  Binds []string `json:"Binds"`
  RestartPolicy struct{Name string `json:"Name"`} `json:"RestartPolicy"`
  NetworkMode string `json:"NetworkMode"`
  CgroupnsMode string `json:"CgroupnsMode"`
  CapAdd []string `json:"CapAdd"`
  DNS []string `json:"Dns"`
  Privileged bool `json:"Privileged"`
  AutoRemove bool `json:"AutoRemove"`
  PidMode string `json:"PidMode"`
  PortBindings map[string]any `json:"PortBindings"`
  Devices []any `json:"Devices"`
 } `json:"HostConfig"`
}

func verifyTopology(d dockerInspect)error{
 if d.ID==""||d.Name!="/"+agentName||!d.State.Running{return errors.New("exact running RunnerHub agent required")}
 if !strings.HasPrefix(d.Config.Image,agentupdate.AgentImage+":")&&!strings.HasPrefix(d.Config.Image,agentupdate.AgentImage+"@sha256:"){
  return errors.New("unrecognized agent image")
 }
 if !slices.Equal(d.Config.Entrypoint,[]string{entrypoint})||len(d.Config.Cmd)!=0{return errors.New("custom entrypoint/command is unsupported")}
 // Container-specific labels are not part of the standard bootstrap
 // topology. Refuse them instead of passing arbitrary values (which may
 // contain secrets) through process arguments during replacement.
 for key:=range d.Config.Labels {
  if !strings.HasPrefix(key,"org.opencontainers.image.") {
   return errors.New("unrecognized agent container label")
  }
 }
 h:=d.HostConfig
 if h.RestartPolicy.Name!="unless-stopped"||h.Privileged||h.AutoRemove||h.PidMode!=""||
  (h.NetworkMode!=""&&h.NetworkMode!="default"&&h.NetworkMode!="bridge")||
  len(h.PortBindings)!=0||len(h.Devices)!=0{
  return errors.New("unsupported Docker agent restart, namespace, networking, or privileges")
 }
 seen:=map[string]bool{}
 cgroup:=false
 for _,b:=range h.Binds{
  switch b{
  case "/var/run/docker.sock:/var/run/docker.sock":
   if seen["docker"]{return errors.New("duplicate Docker socket mount")};seen["docker"]=true
  case "/var/lib/runnerhub:/var/lib/runnerhub":
   if seen["state"]{return errors.New("duplicate state mount")};seen["state"]=true
  case "/sys/fs/cgroup:/sys/fs/cgroup-host:rw":
   if cgroup{return errors.New("duplicate cgroup mount")};cgroup=true
  default:return errors.New("unrecognized agent mount")
  }
 }
 if !seen["docker"]||!seen["state"]{return errors.New("missing persistent agent mount or Docker socket")}
 if cgroup {
  if h.CgroupnsMode!="host"||!slices.Equal(h.CapAdd,[]string{"SYS_ADMIN"}){return errors.New("unsupported DinD cgroup isolation")}
 }else if len(h.CapAdd)>0||h.CgroupnsMode=="host"{return errors.New("unrecognized cgroup privileges")}
 env:=map[string]string{}
 for _,v:=range d.Config.Env{
  key,value,ok:=strings.Cut(v,"=")
  if !ok||key==""||strings.ContainsAny(key,"\r\n"){return errors.New("invalid container environment")}
  env[key]=value
 }
 if env["RUNNERHUB_PROVIDER"]!="docker"||env["RUNNERHUB_CONTROL_PLANE_URL"]==""||env["RUNNERHUB_HOST_NAME"]==""{
  return errors.New("missing Docker agent identity and control configuration")
 }
 if env["RUNNERHUB_ENROLLMENT_TOKEN"]!=""||env["RUNNERHUB_ENROLLMENT_TOKEN_FILE"]!=""{
  return errors.New("temporary enrollment token must be removed before updating")
 }
 return nil
}
type storedCredential struct{HostID string `json:"hostId"`}

// Adapter is intended to run in a separate privileged helper process with a
// Docker socket, its own lifecycle, and read-only credential-file access.
// It is deliberately not connected to any public/control-plane route yet.
type Adapter struct{
 Docker CLI
 CredentialFile string
 OperationID string
 JournalDir string
 Heartbeat func(context.Context,string,string,time.Time)error
 mu sync.Mutex
 captured map[string]dockerInspect
 stages map[string]dockerInspect
}

func(a *Adapter)client()CLI{if a.Docker!=nil{return a.Docker};return ExecCLI{}}
func(a *Adapter)journal()JournalStore{return JournalStore{Dir:a.JournalDir}}
func(a *Adapter)writeRecoveryState(state string)error{
 if a.OperationID==""{return nil}
 r,err:=a.journal().Load(a.OperationID)
 if err!=nil{return err}
 r.State=state
 return a.journal().Save(r)
}
func(a *Adapter)RecordCompletion(state string)error{
 if state!="succeeded"&&state!="rolled-back"&&state!="review"{
  return errors.New("invalid terminal update state")
 }
 return a.writeRecoveryState(state)
}
func(a *Adapter)inspect(ctx context.Context,id string)(dockerInspect,error){
 out,err:=a.client().Run(ctx,"inspect",id)
 if err!=nil{return dockerInspect{},err}
 var arr []dockerInspect
 if err:=json.Unmarshal(out,&arr);err!=nil||len(arr)!=1{return dockerInspect{},errors.New("Docker inspect did not return exactly one container")}
 return arr[0],nil
}
func(a *Adapter)Capture(ctx context.Context,hostID string)(agentupdate.Snapshot,error){
 path:=a.CredentialFile
 if path==""{path="/var/lib/runnerhub/agent-credentials.json"}
 if filepath.Clean(path)!=path{return agentupdate.Snapshot{},errors.New("invalid agent credential path")}
 raw,err:=os.ReadFile(path)
 if err!=nil{return agentupdate.Snapshot{},fmt.Errorf("agent identity unavailable: %w",err)}
 var cred storedCredential
 if json.Unmarshal(raw,&cred)!=nil||cred.HostID==""||cred.HostID!=hostID{return agentupdate.Snapshot{},errors.New("agent credential does not match selected host ID")}
 d,err:=a.inspect(ctx,agentName)
 if err!=nil{return agentupdate.Snapshot{},err}
 if err:=verifyTopology(d);err!=nil{return agentupdate.Snapshot{},err}
 a.mu.Lock()
 if a.captured==nil{a.captured=map[string]dockerInspect{}}
 a.captured[d.ID]=d
 a.mu.Unlock()
 return agentupdate.Snapshot{HostID:hostID,ContainerID:d.ID,Image:d.ImageID,Running:d.State.Running,SafeTopology:true},nil
}
func(a *Adapter)PullAndVerify(ctx context.Context,target agentupdate.Target)error{
 if err:=target.Validate();err!=nil{return err}
 if _,err:=a.client().Run(ctx,"pull",target.Image);err!=nil{return err}
 out,err:=a.client().Run(ctx,"image","inspect","--format","{{.Id}}",target.Image)
 if err!=nil||strings.TrimSpace(string(out))==""{return errors.New("published agent image could not be verified locally")}
 // The exact source revision must be bound to the *pulled image*, rather
 // than trusted merely because the control plane supplied a 40-hex value.
 // The release pipeline writes OCI labels using docker/metadata-action.
 labelsJSON,err:=a.client().Run(ctx,"image","inspect","--format","{{json .Config.Labels}}",target.Image)
 if err!=nil{return errors.New("published agent OCI revision metadata unavailable")}
 var labels map[string]string
 if err:=json.Unmarshal(labelsJSON,&labels);err!=nil{
  return errors.New("published agent OCI labels are invalid")
 }
 if labels["org.opencontainers.image.revision"]!=target.Revision ||
  labels["org.opencontainers.image.source"]!="https://github.com/eutelic/runnerhub"{
  return errors.New("published agent image revision or source does not match approved release")
 }
 return nil
}
func(a *Adapter)StageReplacement(ctx context.Context,prior agentupdate.Snapshot,target agentupdate.Target)(agentupdate.Stage,error){
 if err:=target.Validate();err!=nil{return agentupdate.Stage{},err}
 a.mu.Lock();d,ok:=a.captured[prior.ContainerID];a.mu.Unlock()
 if !ok||d.ID!=prior.ContainerID||!prior.SafeTopology{return agentupdate.Stage{},errors.New("trusted agent snapshot missing")}
 if err:=verifyTopology(d);err!=nil{return agentupdate.Stage{},err}
 // Environment includes connection settings; never place their values in
 // a Docker command argument or in an audit log.
 envFile,err:=os.CreateTemp("","runnerhub-agent-update-env-*")
 if err!=nil{return agentupdate.Stage{},err}
 defer os.Remove(envFile.Name())
 if err:=envFile.Chmod(0600);err!=nil{envFile.Close();return agentupdate.Stage{},err}
 for _,line:=range d.Config.Env{
  if strings.ContainsAny(line,"\r\n"){envFile.Close();return agentupdate.Stage{},errors.New("multiline agent environment unsupported")}
  if _,err:=envFile.WriteString(line+"\n");err!=nil{envFile.Close();return agentupdate.Stage{},err}
 }
 if err:=envFile.Close();err!=nil{return agentupdate.Stage{},err}
 var token [8]byte
 if _,err:=rand.Read(token[:]);err!=nil{return agentupdate.Stage{},err}
 name:="runnerhub-agent-stage-"+hex.EncodeToString(token[:])
 args:=[]string{"create","--name",name,"--restart","no","--env-file",envFile.Name()}
 for _,b:=range d.HostConfig.Binds{args=append(args,"--volume",b)}
 if d.HostConfig.CgroupnsMode!=""{args=append(args,"--cgroupns",d.HostConfig.CgroupnsMode)}
 for _,v:=range d.HostConfig.CapAdd{args=append(args,"--cap-add",v)}
 for _,v:=range d.HostConfig.DNS{args=append(args,"--dns",v)}
 // Image-origin OCI labels are furnished by the new image; no existing
 // arbitrary container labels are emitted as Docker command arguments.
 args=append(args,target.Image)
 out,err:=a.client().Run(ctx,args...)
 if err!=nil{return agentupdate.Stage{},err}
 id:=strings.TrimSpace(string(out))
 if len(id)!=64{return agentupdate.Stage{},errors.New("Docker create did not return a full container ID")}
 // Retain the newly created ID before inspection. If Docker create
 // succeeded but inspect failed, the transaction can still discard this
 // precise container; an unrelated name is never removed.
 a.mu.Lock()
 if a.stages==nil{a.stages=map[string]dockerInspect{}}
 a.stages[id]=dockerInspect{ID:id,Name:"/"+name}
 a.mu.Unlock()
 staged,err:=a.inspect(ctx,id)
 if err!=nil{return agentupdate.Stage{ContainerID:id},err}
 if staged.ID!=id||staged.Name!="/"+name||staged.State.Running||staged.Config.Image!=target.Image{
  return agentupdate.Stage{ContainerID:id},errors.New("staged agent identity or image mismatch")
 }
 a.mu.Lock();a.stages[id]=staged;a.mu.Unlock()
 if a.OperationID!=""{
  r:=RecoveryRecord{Version:1,HostID:prior.HostID,OperationID:a.OperationID,
   OriginalContainerID:prior.ContainerID,OriginalImageID:prior.Image,
   ReplacementContainerID:id,TargetImage:target.Image,TargetRevision:target.Revision,
   State:"staged"}
  if err:=a.journal().Save(r);err!=nil{
   // Stage is known and still stopped. The caller can discard precisely
   // this container; it must not proceed to swap without a durable journal.
   return agentupdate.Stage{ContainerID:id},err
  }
 }
 return agentupdate.Stage{ContainerID:id},nil
}
func(a *Adapter)trusted(prior agentupdate.Snapshot,stage agentupdate.Stage)(dockerInspect,dockerInspect,error){
 a.mu.Lock();defer a.mu.Unlock()
 old,okOld:=a.captured[prior.ContainerID]
 next,okNext:=a.stages[stage.ContainerID]
 if !okOld||!okNext||old.ID!=prior.ContainerID||next.ID!=stage.ContainerID||old.ID==next.ID{
  return dockerInspect{},dockerInspect{},errors.New("update container identity not found in helper")
 }
 return old,next,nil
}
func(a *Adapter)Discard(ctx context.Context,prior agentupdate.Snapshot,stage agentupdate.Stage)error{
 _,_,err:=a.trusted(prior,stage);if err!=nil{return err}
 return a.removeIfSafe(ctx,stage.ContainerID)
}
func(a *Adapter)removeIfSafe(ctx context.Context,id string)error{
 d,err:=a.inspect(ctx,id);if err!=nil{return err}
 if d.State.Running{return errors.New("refusing to delete a running agent")}
 _,err=a.client().Run(ctx,"rm",id)
 return err
}
func(a *Adapter)Replace(ctx context.Context,prior agentupdate.Snapshot,stage agentupdate.Stage)error{
 old,next,err:=a.trusted(prior,stage);if err!=nil{return err}
 current,err:=a.inspect(ctx,old.ID)
 if err!=nil||current.Name!="/"+agentName||!current.State.Running{return errors.New("existing agent changed before swap")}
 staged,err:=a.inspect(ctx,next.ID)
 if err!=nil||staged.State.Running||staged.Name!=next.Name{return errors.New("staged container changed before swap")}
 backup:="runnerhub-agent-backup-"+old.ID[:12]
 // Journal the impending disruptive change before touching restart policy.
 // If this cannot be persisted, the running old agent is untouched.
 if err:=a.writeRecoveryState("replacing");err!=nil{return err}
 if _,err:=a.client().Run(ctx,"update","--restart=no",old.ID);err!=nil{return err}
 if _,err:=a.client().Run(ctx,"stop","--time","30",old.ID);err!=nil{return err}
 if _,err:=a.client().Run(ctx,"rename",old.ID,backup);err!=nil{return err}
 if _,err:=a.client().Run(ctx,"rename",next.ID,agentName);err!=nil{return err}
 if _,err:=a.client().Run(ctx,"update","--restart=unless-stopped",next.ID);err!=nil{return err}
 _,err=a.client().Run(ctx,"start",next.ID)
 return err
}
func(a *Adapter)VerifyRunning(ctx context.Context,stage agentupdate.Stage,target agentupdate.Target)error{
 if err:=target.Validate();err!=nil{return err}
 d,err:=a.inspect(ctx,stage.ContainerID)
 if err!=nil{return err}
 if !d.State.Running||d.Name!="/"+agentName||d.Config.Image!=target.Image{return errors.New("updated agent is not the expected running container")}
 out,err:=a.client().Run(ctx,"image","inspect","--format","{{.Id}}",target.Image)
 if err!=nil||strings.TrimSpace(string(out))!=d.ImageID{return errors.New("running agent image ID differs from pinned target")}
 return nil
}
func(a *Adapter)WaitForHeartbeat(ctx context.Context,hostID,revision string,after time.Time)error{
 if a.Heartbeat==nil{return errors.New("verified same-host-ID heartbeat source is not configured")}
 return a.Heartbeat(ctx,hostID,revision,after)
}
func(a *Adapter)Restore(ctx context.Context,prior agentupdate.Snapshot,stage agentupdate.Stage)error{
 old,next,err:=a.trusted(prior,stage);if err!=nil{return err}
 // Verify both exact Docker identities before the first destructive step.
 // An unrecognized backup name must never cause us to remove the only
 // running replacement container and then discover the original is lost.
 original,err:=a.inspect(ctx,old.ID)
 if err!=nil||original.ID!=old.ID||original.ImageID!=prior.Image||
  (original.Name!="/"+agentName&&original.Name!="/runnerhub-agent-backup-"+old.ID[:12]){
  return errors.New("original agent identity is not safe for rollback")
 }
 d,err:=a.inspect(ctx,next.ID)
 if err!=nil||d.ID!=next.ID||
  (d.Name!="/"+agentName&&d.Name!=next.Name)||
  d.Config.Image!=next.Config.Image{
  return errors.New("replacement agent identity is not safe for rollback")
 }
 // A recovery journal write error must never prevent an attempted
 // rollback: the old agent may already be stopped.
 _=a.writeRecoveryState("rolling-back")
 if d.State.Running{
  if d.Name!="/"+agentName{return errors.New("running candidate has unexpected name")}
  if _,err:=a.client().Run(ctx,"stop","--time","30",next.ID);err!=nil{return err}
 }
 if _,err:=a.client().Run(ctx,"rm",next.ID);err!=nil{return err}
 d,err=a.inspect(ctx,old.ID)
 if err!=nil{return err}
 if d.Name!="/"+agentName{
  if d.Name!="/runnerhub-agent-backup-"+old.ID[:12]{return errors.New("old agent backup has unexpected name")}
  if _,err:=a.client().Run(ctx,"rename",old.ID,agentName);err!=nil{return err}
 }
 if _,err:=a.client().Run(ctx,"update","--restart=unless-stopped",old.ID);err!=nil{return err}
 if !d.State.Running{
  if _,err:=a.client().Run(ctx,"start",old.ID);err!=nil{return err}
 }
 recovered,err:=a.inspect(ctx,old.ID)
 if err!=nil||!recovered.State.Running||recovered.Name!="/"+agentName||recovered.ImageID!=prior.Image{
  return errors.New("old agent rollback could not be confirmed")
 }
 return a.writeRecoveryState("rolled-back")
}
func(a *Adapter)Finalize(ctx context.Context,prior agentupdate.Snapshot,stage agentupdate.Stage)error{
 old,_,err:=a.trusted(prior,stage);if err!=nil{return err}
 active,err:=a.inspect(ctx,stage.ContainerID)
 if err!=nil||!active.State.Running||active.Name!="/"+agentName{return errors.New("new agent is not active; backup retained")}
 d,err:=a.inspect(ctx,old.ID)
 if err!=nil||d.State.Running||d.Name!="/runnerhub-agent-backup-"+old.ID[:12]{
  return errors.New("old agent backup is not safely retained")
 }
 // Do not remove the old container yet. A successful Docker swap can
 // precede the authenticated control-plane completion POST, and that POST
 // may fail or the helper may crash. Retaining the stopped backup permits
 // an operator or future watchdog to restore the previous agent by its
 // original Docker ID without ever guessing at its environment/config.
 // Cleanup must be a separate explicit *post-acknowledgment* operation.
 if d.ImageID!=prior.Image{return errors.New("old backup image differs from captured agent")}
 return a.writeRecoveryState("awaiting-completion")
}
