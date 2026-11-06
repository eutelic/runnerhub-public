package agentupdatedocker

import(
 "os"
 "path/filepath"
 "strings"
 "testing"
 "github.com/eutelic/runnerhub/internal/agentupdate"
)

func sampleRecoveryRecord()RecoveryRecord{
 return RecoveryRecord{
  Version:1,HostID:"host-compute",OperationID:"update-123",
  OriginalContainerID:oldID,OriginalImageID:"sha256:original",
  ReplacementContainerID:newID,
  TargetImage:agentupdate.AgentImage+"@sha256:"+strings.Repeat("c",64),
  TargetRevision:strings.Repeat("d",40),State:"staged",
 }
}
func TestRecoveryJournalPersistsOnlyPrivateNonsecretState(t *testing.T){
 dir:=filepath.Join(t.TempDir(),"journal")
 st:=JournalStore{Dir:dir}
 rec:=sampleRecoveryRecord()
 if err:=st.Save(rec);err!=nil{t.Fatal(err)}
 path,_:=st.path(rec.OperationID)
 fi,err:=os.Lstat(path)
 if err!=nil{t.Fatal(err)}
 if fi.Mode().Perm()!=0600{t.Fatalf("journal must be private: %v",fi.Mode())}
 di,err:=os.Lstat(dir)
 if err!=nil||di.Mode().Perm()!=0700{t.Fatalf("directory permissions: %+v %v",di,err)}
 raw,err:=os.ReadFile(path)
 if err!=nil{t.Fatal(err)}
 if strings.Contains(string(raw),"token")||strings.Contains(string(raw),"credential")||strings.Contains(string(raw),"ENV="){
  t.Fatal("secret-like fields escaped to recovery journal")
 }
 stored,err:=st.Load(rec.OperationID)
 if err!=nil||stored.OriginalContainerID!=oldID||stored.ReplacementContainerID!=newID{
  t.Fatalf("journal identity was lost: %+v %v",stored,err)
 }
 rec.State="replacing"
 if err:=st.Save(rec);err!=nil{t.Fatalf("stage transition failed: %v",err)}
 rec.State="succeeded"
 if err:=st.Save(rec);err!=nil{t.Fatal(err)}
 rec.State="replacing"
 if err:=st.Save(rec);err==nil{t.Fatal("terminal journal reopened")}
}
func TestRecoveryJournalRejectsIdentityReuseAndSymlinks(t *testing.T){
 st:=JournalStore{Dir:filepath.Join(t.TempDir(),"journal")}
 rec:=sampleRecoveryRecord()
 if err:=st.Save(rec);err!=nil{t.Fatal(err)}
 changed:=rec
 changed.ReplacementContainerID=strings.Repeat("f",64)
 if err:=st.Save(changed);err==nil{t.Fatal("operation hijacked another container")}
 invalid:=rec
 invalid.OperationID="../../etc/passwd"
 if err:=st.Save(invalid);err==nil{t.Fatal("path traversal accepted")}
 path,_:=st.path(rec.OperationID)
 if err:=os.Remove(path);err!=nil{t.Fatal(err)}
 target:=filepath.Join(t.TempDir(),"journal-target")
 if err:=os.WriteFile(target,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
 if err:=os.Symlink(target,path);err!=nil{t.Fatal(err)}
 if err:=st.Save(rec);err==nil{t.Fatal("symlink journal overwritten")}
 if _,err:=st.Load(rec.OperationID);err==nil{t.Fatal("symlink journal read")}
}
func TestRecoveryJournalFailsClosedWithoutPrivateDirectory(t *testing.T){
 dir:=filepath.Join(t.TempDir(),"journal")
 if err:=os.Mkdir(dir,0755);err!=nil{t.Fatal(err)}
 if err:=os.Chmod(dir,0755);err!=nil{t.Fatal(err)} // do not depend on the CI process umask
 if err:=(JournalStore{Dir:dir}).Save(sampleRecoveryRecord());err==nil{
  t.Fatal("world-readable journal directory accepted")
 }
}
