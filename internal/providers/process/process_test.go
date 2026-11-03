package process

import (
    "context"
    "runtime"
    "strings"
    "testing"

    "github.com/eutelic/runnerhub/internal/core"
    "github.com/eutelic/runnerhub/internal/provider"
)

func TestNativeAndroidEmulatorRechecksKVMBeforeLaunch(t *testing.T) {
    p,err:=New(Config{RunnerDir:t.TempDir(),StateDir:t.TempDir()})
    if err!=nil {t.Fatal(err)}
    p.kvmReady=func()bool{return false}
    p.availableResources=func()core.Resources{
        return core.Resources{CPUMillis:12000,MemoryBytes:32<<30,DiskBytes:100<<30}
    }
    spec:=provider.WorkerSpec{
        WorkerID:"w",PoolID:"p",RunnerName:"runner",JITConfig:"not-real-jit",
        Labels:[]string{core.AndroidEmulatorScaleSet},
        Isolation:core.IsolationHostPersistent,
        Resources:core.Resources{CPUMillis:4000,MemoryBytes:8<<30,DiskBytes:20<<30},
    }
    if err:=p.Validate(context.Background(),spec);err==nil||!strings.Contains(err.Error(),"KVM") {
        t.Fatalf("missing KVM should prevent launch: %v",err)
    }
    ordinary:=spec
    ordinary.Labels=[]string{"runnerhub"}
    if err:=p.Validate(context.Background(),ordinary);err!=nil {
        t.Fatalf("ordinary native job should not require KVM: %v",err)
    }
    if runtime.GOOS=="linux" && runtime.GOARCH=="amd64" {
        p.kvmReady=func()bool{return true}
        if err:=p.Validate(context.Background(),spec);err!=nil {
            t.Fatalf("verified Linux amd64 KVM job should be admitted: %v",err)
        }
        p.availableResources=func()core.Resources{return core.Resources{CPUMillis:4000,MemoryBytes:4<<30,DiskBytes:100<<30}}
        if err:=p.Validate(context.Background(),spec);err==nil||!strings.Contains(err.Error(),"capacity") {
            t.Fatalf("reduced host RAM must prevent job: %v",err)
        }
        p.availableResources=func()core.Resources{return core.Resources{CPUMillis:12000,MemoryBytes:32<<30,DiskBytes:100<<30}}
        spec.Resources.CPUMillis=2000
        if err:=p.Validate(context.Background(),spec);err==nil||!strings.Contains(err.Error(),"reservation") {
            t.Fatalf("undersized reservation must prevent job: %v",err)
        }
    }
}

func TestCapabilityAdvertisementDoesNotTrustUserTags(t *testing.T) {
    p,err:=New(Config{RunnerDir:t.TempDir(),StateDir:t.TempDir()})
    if err!=nil{t.Fatal(err)}
    p.kvmReady=func()bool{return false}
    p.availableResources=func()core.Resources{return core.Resources{}}
    caps,err:=p.Capabilities(context.Background())
    if err!=nil{t.Fatal(err)}
    if caps.Features["kvm"] || caps.Labels[core.AndroidEmulatorCapabilityTag]=="true" {
        t.Fatal("unavailable KVM must not be advertised as a system capability")
    }
    if runtime.GOOS=="linux" && runtime.GOARCH=="amd64" {
        p.kvmReady=func()bool{return true}
        caps,err=p.Capabilities(context.Background())
        if err!=nil{t.Fatal(err)}
        if !caps.Features["kvm"] || caps.Labels[core.AndroidEmulatorCapabilityTag]!="true" {
            t.Fatal("verified Linux/amd64 KVM must be advertised")
        }
    }
}

func TestDedicatedNativeAgentRejectsOrdinaryJobs(t *testing.T) {
 p,err:=New(Config{RunnerDir:t.TempDir(),StateDir:t.TempDir(),AndroidEmulatorOnly:true})
 if err!=nil{t.Fatal(err)}
 p.kvmReady=func()bool{return true}
 p.availableResources=func()core.Resources{return core.Resources{CPUMillis:12000,MemoryBytes:32<<30,DiskBytes:100<<30}}
 spec:=provider.WorkerSpec{
   WorkerID:"non-emulator",PoolID:"pool",RunnerName:"runner",
   JITConfig:"fake-but-present",Isolation:core.IsolationHostPersistent,
   Labels:[]string{"runnerhub"},
   Resources:core.Resources{CPUMillis:4000,MemoryBytes:8<<30,DiskBytes:20<<30},
 }
 if err:=p.Validate(context.Background(),spec);err==nil||!strings.Contains(err.Error(),"non-emulator"){
    t.Fatalf("dedicated native agent accepted non-emulator job: %v",err)
 }
 spec.Labels=[]string{core.AndroidEmulatorScaleSet}
 if runtime.GOOS=="linux"&&runtime.GOARCH=="amd64"{
    if err:=p.Validate(context.Background(),spec);err!=nil{t.Fatalf("trusted emulator job rejected: %v",err)}
 }
}

func TestDedicatedNativeCapabilityTagCannotBeForged(t *testing.T) {
 p,err:=New(Config{RunnerDir:t.TempDir(),StateDir:t.TempDir(),AndroidEmulatorOnly:true})
 if err!=nil{t.Fatal(err)}
 p.kvmReady=func()bool{return false}
 cap,err:=p.Capabilities(context.Background())
 if err!=nil{t.Fatal(err)}
 if cap.Labels[core.AndroidEmulatorDedicatedTag]!="true"{t.Fatal("dedicated native host did not advertise system constraint")}
 if cap.Labels[core.AndroidEmulatorCapabilityTag]=="true"{t.Fatal("dedicated mode must not claim missing KVM")}
}
