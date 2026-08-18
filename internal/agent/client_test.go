package agent

import "testing"
func TestControlPlaneURLRequiresProtectedTransport(t *testing.T){base:=ClientConfig{EnrollmentToken:"1234567890123456",Name:"host",Provider:"docker"};base.ControlPlaneURL="http://10.0.0.5:8080";if _,err:=NewClient(base,nil);err==nil{t.Fatal("remote plaintext control plane accepted")};base.AllowInsecureHTTP=true;if _,err:=NewClient(base,nil);err!=nil{t.Fatalf("explicit protected-network override rejected: %v",err)};base.AllowInsecureHTTP=false;base.ControlPlaneURL="http://127.0.0.1:8080";if _,err:=NewClient(base,nil);err!=nil{t.Fatalf("loopback http rejected: %v",err)};base.ControlPlaneURL="https://runnerhub.example.com";if _,err:=NewClient(base,nil);err!=nil{t.Fatalf("https rejected: %v",err)}}
