package agentupdatedocker

import (
 "encoding/json"
 "github.com/eutelic/runnerhub/internal/agentupdate"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "regexp"
 "time"
)

const DefaultJournalDir="/var/lib/runnerhub/agent-update-operations"

var exactContainerID=regexp.MustCompile(`^[a-f0-9]{64}$`)

// RecoveryRecord is deliberately limited to nonsecret Docker identifiers.
// Never persist environment, credential values, Docker inspect output, or
// registry authentication in the host-side recovery journal.
type RecoveryRecord struct{
 Version int `json:"version"`
 HostID string `json:"hostId"`
 OperationID string `json:"operationId"`
 OriginalContainerID string `json:"originalContainerId"`
 OriginalImageID string `json:"originalImageId"`
 ReplacementContainerID string `json:"replacementContainerId"`
 TargetImage string `json:"targetImage"`
 TargetRevision string `json:"targetRevision"`
 State string `json:"state"`
 UpdatedAt time.Time `json:"updatedAt"`
}

func validateRecoveryRecord(r RecoveryRecord)error{
 if r.Version!=1||r.HostID==""||!safeOperationID.MatchString(r.OperationID)||
  !exactContainerID.MatchString(r.OriginalContainerID)||
  !exactContainerID.MatchString(r.ReplacementContainerID)||
  r.OriginalContainerID==r.ReplacementContainerID||
  r.OriginalImageID==""{
  return errors.New("incomplete or unsafe updater recovery identifiers")
 }
 if err:=(agentUpdateTarget(r.TargetImage,r.TargetRevision)).Validate();err!=nil{return err}
 switch r.State{
 case "staged","replacing","rolling-back","rolled-back","awaiting-completion","succeeded","review":
  return nil
 default:return errors.New("invalid updater recovery state")
 }
}

// agentUpdateTarget is a private alias to keep the validation in one place.
func agentUpdateTarget(image,revision string) agentupdate.Target{
 return agentupdate.Target{Image:image,Revision:revision}
}

type JournalStore struct{Dir string}
func(s JournalStore)root()string{
 if s.Dir!=""{return s.Dir}
 return DefaultJournalDir
}
func(s JournalStore)path(operationID string)(string,error){
 if !safeOperationID.MatchString(operationID){return "",errors.New("unsafe operation ID")}
 return filepath.Join(s.root(),"update-"+operationID+".json"),nil
}
func(s JournalStore)Load(operationID string)(RecoveryRecord,error){
 path,err:=s.path(operationID)
 if err!=nil{return RecoveryRecord{},err}
 fi,err:=os.Lstat(path)
 if err!=nil{return RecoveryRecord{},err}
 if !fi.Mode().IsRegular()||fi.Mode().Perm()&0077!=0{
  return RecoveryRecord{},errors.New("recovery journal is not a private regular file")
 }
 b,err:=os.ReadFile(path)
 if err!=nil{return RecoveryRecord{},err}
 var r RecoveryRecord
 if err:=json.Unmarshal(b,&r);err!=nil{return RecoveryRecord{},err}
 if err:=validateRecoveryRecord(r);err!=nil{return RecoveryRecord{},err}
 if r.OperationID!=operationID{return RecoveryRecord{},errors.New("journal operation ID mismatch")}
 return r,nil
}
func(s JournalStore)Save(r RecoveryRecord)error{
 if err:=validateRecoveryRecord(r);err!=nil{return err}
 dir:=s.root()
 if err:=os.MkdirAll(dir,0700);err!=nil{return err}
 fi,err:=os.Lstat(dir)
 if err!=nil{return err}
 if !fi.IsDir()||fi.Mode().Perm()&0077!=0{return errors.New("updater journal directory must be private and not a symlink")}
 path,err:=s.path(r.OperationID)
 if err!=nil{return err}
 prior,err:=s.Load(r.OperationID)
 if err==nil{
  if prior.HostID!=r.HostID||prior.OriginalContainerID!=r.OriginalContainerID||
   prior.ReplacementContainerID!=r.ReplacementContainerID||
   prior.OriginalImageID!=r.OriginalImageID||
   prior.TargetImage!=r.TargetImage||prior.TargetRevision!=r.TargetRevision{
   return errors.New("recovery operation identity was changed")
  }
  if prior.State=="succeeded"||prior.State=="rolled-back"{
   if prior.State!=r.State{return errors.New("cannot reopen completed recovery journal")}
  }
 }else if !errors.Is(err,os.ErrNotExist){return fmt.Errorf("untrusted existing recovery journal: %w",err)}
 r.UpdatedAt=time.Now().UTC()
 data,err:=json.Marshal(r)
 if err!=nil{return err}
 file,err:=os.CreateTemp(dir,".agent-update-*")
 if err!=nil{return err}
 tmp:=file.Name()
 defer os.Remove(tmp)
 if err:=file.Chmod(0600);err!=nil{file.Close();return err}
 if _,err:=file.Write(data);err!=nil{file.Close();return err}
 if err:=file.Sync();err!=nil{file.Close();return err}
 if err:=file.Close();err!=nil{return err}
 if err:=os.Rename(tmp,path);err!=nil{return err}
 d,err:=os.Open(dir)
 if err!=nil{return err}
 defer d.Close()
 return d.Sync()
}
