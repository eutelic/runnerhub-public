package scheduler

import (
    "context"
    "errors"
    "strings"
    "testing"

    "github.com/eutelic/runnerhub/internal/core"
    "github.com/eutelic/runnerhub/internal/store"
)

func TestAndroidEmulatorIsNeverSentToDockerOrSpoofedTag(t *testing.T) {
    pool:=core.RunnerPool{
        ID:"android",Name:"kinetic-android",Labels:[]string{core.AndroidEmulatorScaleSet},
        ScaleSetName:core.AndroidEmulatorScaleSet,
        Isolation:core.IsolationHostPersistent,
        HostSelector:map[string]string{"android-emulator":"true",core.AndroidEmulatorCapabilityTag:"true"},
        Resources:core.Resources{CPUMillis:4000,MemoryBytes:8<<30,DiskBytes:20<<30},
        MaxConcurrency:1,
    }
    h:=core.Host{
        ID:"host",Name:"host",Provider:"docker",OS:"linux",Architecture:"amd64",
        State:core.HostOnline,Tags:map[string]string{"android-emulator":"true",core.AndroidEmulatorCapabilityTag:"true","isolation.host-persistent":"true"},
        Capacity:core.Resources{CPUMillis:10000,MemoryBytes:32<<30,DiskBytes:100<<30},
    }
    cases:=[]struct{name string; edit func(*core.Host);want string}{
        {"Docker worker",func(h *core.Host){},"process host"},
        {"Spoofed user tag",func(h *core.Host){h.Provider="process"},"KVM unavailable"},
        {"Wrong architecture",func(h *core.Host){h.Provider="process";h.Architecture="arm64";h.SystemTags=map[string]string{core.AndroidEmulatorCapabilityTag:"true"}},"Linux amd64"},
        {"No KVM access",func(h *core.Host){h.Provider="process";h.SystemTags=nil},"KVM unavailable"},
    }
    for _,tc:=range cases{
        t.Run(tc.name,func(t *testing.T){
            host:=h
            tc.edit(&host)
            reason:=HostRejectionReason(host,pool,core.Resources{})
            if !strings.Contains(reason,tc.want){t.Fatalf("rejection=%q want %q",reason,tc.want)}
            s:=SummarizeCapacity(pool,[]core.Host{host},nil)
            if s.CompatibleHosts!=0 || s.AdditionalWorkers!=0 || s.Bottleneck!="no_compatible_host"{
                t.Fatalf("incorrect compatible capacity %+v",s)
            }
        })
    }
    h.Provider="process"
    h.SystemTags=map[string]string{core.AndroidEmulatorCapabilityTag:"true"}
    if reason:=HostRejectionReason(h,pool,core.Resources{});reason!=""{t.Fatalf("verified host rejected: %s",reason)}
    st,err:=store.OpenSQLite(":memory:")
    if err!=nil{t.Fatal(err)}
    defer st.Close()
    if err:=st.UpsertHost(context.Background(),h);err!=nil{t.Fatal(err)}
    dec,err:=(Scheduler{Store:st}).SelectHost(context.Background(),pool)
    if err!=nil || dec.Host.ID!="host" {t.Fatalf("host=%+v err=%v",dec.Host,err)}
    h.SystemTags=nil
    if err:=st.UpsertHost(context.Background(),h);err!=nil{t.Fatal(err)}
    _,err=(Scheduler{Store:st}).SelectHost(context.Background(),pool)
    if !errors.Is(err,ErrNoCapacity){t.Fatalf("missing KVM should reject: %v",err)}
}

func TestDedicatedNativeKVMHostIsNotARegularProcessPoolCandidate(t *testing.T) {
 h:=core.Host{
   ID:"native",Name:"compute-android",Provider:"process",State:core.HostOnline,
   OS:"linux",Architecture:"amd64",
   SystemTags:map[string]string{core.AndroidEmulatorDedicatedTag:"true",core.AndroidEmulatorCapabilityTag:"true"},
   Tags:map[string]string{core.AndroidEmulatorDedicatedTag:"true",core.AndroidEmulatorCapabilityTag:"true","isolation.host-persistent":"true"},
   Capacity:core.Resources{CPUMillis:12000,MemoryBytes:32<<30,DiskBytes:100<<30},
 }
 generic:=core.RunnerPool{
   ID:"other",Name:"other",ScaleSetName:"runnerhub-native",
   Isolation:core.IsolationHostPersistent,
   Resources:core.Resources{CPUMillis:2000,MemoryBytes:4<<30,DiskBytes:10<<30},
 }
 if reason:=HostRejectionReason(h,generic,core.Resources{});!strings.Contains(reason,"only accepts Android emulator"){
   t.Fatalf("dedicated KVM host incorrectly eligible for normal native pool: %q",reason)
 }
 summary:=SummarizeCapacity(generic,[]core.Host{h},nil)
 if summary.CompatibleHosts!=0 {t.Fatalf("dedicated KVM host falsely counted: %+v",summary)}
}
