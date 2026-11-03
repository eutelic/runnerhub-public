package main

import (
 "os"
 "path/filepath"
 "testing"
)

func TestReadEnrollmentTokenFromFile(t *testing.T) {
 t.Setenv("RUNNERHUB_ENROLLMENT_TOKEN","environment-value")
 file:=filepath.Join(t.TempDir(),"token")
 if err:=os.WriteFile(file,[]byte("file-value\n"),0600);err!=nil{t.Fatal(err)}
 t.Setenv("RUNNERHUB_ENROLLMENT_TOKEN_FILE",file)
 token,err:=readEnrollmentToken()
 if err!=nil||token!="file-value"{t.Fatalf("token=%q err=%v",token,err)}
 if err:=os.Remove(file);err!=nil{t.Fatal(err)}
 token,err=readEnrollmentToken()
 if err!=nil||token!=""{t.Fatalf("removed token file should be optional: token=%q err=%v",token,err)}
}

func TestEnrollmentTokenFileErrorsFailClosed(t *testing.T) {
 t.Setenv("RUNNERHUB_ENROLLMENT_TOKEN_FILE",t.TempDir())
 if _,err:=readEnrollmentToken();err==nil{t.Fatal("directory is not token file")}
 t.Setenv("RUNNERHUB_ENROLLMENT_TOKEN_FILE","")
 t.Setenv("RUNNERHUB_ENROLLMENT_TOKEN","environment-value")
 token,err:=readEnrollmentToken()
 if err!=nil||token!="environment-value"{t.Fatalf("fallback token=%q err=%v",token,err)}
}

func TestEnrollmentTokenFileRejectsUnsafePathsAndModes(t *testing.T) {
 t.Setenv("RUNNERHUB_ENROLLMENT_TOKEN","")
 t.Setenv("RUNNERHUB_ENROLLMENT_TOKEN_FILE","../relative-token")
 if _,err:=readEnrollmentToken();err==nil{t.Fatal("relative token path was accepted")}
 path:=filepath.Join(t.TempDir(),"token")
 if err:=os.WriteFile(path,[]byte("secret"),0644);err!=nil{t.Fatal(err)}
 t.Setenv("RUNNERHUB_ENROLLMENT_TOKEN_FILE",path)
 if _,err:=readEnrollmentToken();err==nil{t.Fatal("world-readable enrollment token accepted")}
 if err:=os.Chmod(path,0600);err!=nil{t.Fatal(err)}
 link:=filepath.Join(t.TempDir(),"link")
 if err:=os.Symlink(path,link);err!=nil{t.Fatal(err)}
 t.Setenv("RUNNERHUB_ENROLLMENT_TOKEN_FILE",link)
 if _,err:=readEnrollmentToken();err==nil{t.Fatal("symlink enrollment token was accepted")}
 t.Setenv("RUNNERHUB_ENROLLMENT_TOKEN_FILE",path)
 value,err:=readEnrollmentToken()
 if err!=nil||value!="secret"{t.Fatalf("private regular token failed: %q %v",value,err)}
}
