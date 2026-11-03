package process

import (
    "os"
    "path/filepath"
    "testing"
)

func TestNativeRunnerStagesFreshJITCopyWithoutPreviousWorkspace(t *testing.T) {
    source:=t.TempDir()
    work:=filepath.Join(source,"_work")
    diag:=filepath.Join(source,"_diag")
    for _,p:=range []string{work,diag} {
        if err:=os.MkdirAll(p,0700);err!=nil{t.Fatal(err)}
    }
    for _,f:=range []string{filepath.Join(work,"secret.txt"),filepath.Join(diag,"session.log"),filepath.Join(source,".credentials")} {
        if err:=os.WriteFile(f,[]byte("sensitive"),0600);err!=nil{t.Fatal(err)}
    }
    if err:=os.WriteFile(filepath.Join(source,"run.sh"),[]byte("#!/bin/sh\nexit 0\n"),0755);err!=nil{t.Fatal(err)}
    dir:=filepath.Join(t.TempDir(),"worker")
    if err:=stageNativeRunner(source,dir);err!=nil{t.Fatal(err)}
    for _,name:=range []string{"_work","_diag",".credentials"}{
        if _,err:=os.Stat(filepath.Join(dir,name));!os.IsNotExist(err){t.Errorf("staged runner retained %q: %v",name,err)}
    }
    info,err:=os.Stat(filepath.Join(dir,"run.sh"))
    if err!=nil||info.Mode().Perm()&0111==0{t.Fatalf("runner script must stay executable: %v %v",info,err)}
    if _,err:=os.Stat(filepath.Join(source,"_work","secret.txt"));err!=nil{t.Fatalf("trusted template was modified: %v",err)}
}

func TestNativeRunnerRejectsUnsafeStagingPathsAndSymlinks(t *testing.T) {
    src:=t.TempDir()
    if err:=stageNativeRunner(src,filepath.Join(src,"inside"));err==nil{
        t.Fatal("staging inside template causes recursively copied runner data")
    }
    if err:=os.Symlink("/etc/passwd",filepath.Join(src,"malicious"));err!=nil{t.Fatal(err)}
    if err:=stageNativeRunner(src,t.TempDir());err==nil{t.Fatal("absolute template symlink should be refused")}
}

func TestNativeRunnerStagesRelativeSymlinksWithinPrivateRoot(t *testing.T) {
 src:=t.TempDir()
 if err:=os.Mkdir(filepath.Join(src,"bin"),0700);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(src,"bin","runner"),[]byte("trusted"),0600);err!=nil{t.Fatal(err)}
 if err:=os.Symlink("runner",filepath.Join(src,"bin","current"));err!=nil{t.Fatal(err)}
 dst:=filepath.Join(t.TempDir(),"copy")
 if err:=stageNativeRunner(src,dst);err!=nil{t.Fatal(err)}
 data,err:=os.ReadFile(filepath.Join(dst,"bin","current"))
 if err!=nil||string(data)!="trusted"{t.Fatalf("relative symlink not preserved: %q %v",data,err)}
}
func TestNativeRunnerRejectsRelativeSymlinkEscape(t *testing.T) {
 src:=t.TempDir()
 if err:=os.Symlink("../outside",filepath.Join(src,"bad"));err!=nil{t.Fatal(err)}
 if err:=stageNativeRunner(src,filepath.Join(t.TempDir(),"target"));err==nil{
  t.Fatal("relative symlink must not escape runner root")
 }
}
