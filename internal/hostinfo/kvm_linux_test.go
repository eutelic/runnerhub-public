//go:build linux

package hostinfo

import (
    "os"
    "path/filepath"
    "testing"
)

func TestKVMProbeRejectsNonKVMDevices(t *testing.T) {
    if kvmUsableAt("/dev/null") { t.Fatal("/dev/null is not KVM") }
    path:=filepath.Join(t.TempDir(),"kvm")
    if err:=os.WriteFile(path,[]byte("not a device"),0600);err!=nil{t.Fatal(err)}
    if kvmUsableAt(path) {t.Fatal("regular file must not pass KVM probe")}
    if kvmUsableAt(filepath.Join(t.TempDir(),"missing")) {t.Fatal("missing device must not pass KVM probe")}
}
