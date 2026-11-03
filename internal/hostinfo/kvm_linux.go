//go:build linux

package hostinfo

import (
    "os"
    "runtime"
    "syscall"
)

const kvmGetAPIVersion = uintptr(0xAE00)

// KVMUsable checks actual /dev/kvm access for this agent's OS user. A
// character-device existence or a machine tag alone is not sufficient.
func KVMUsable() bool {
    return runtime.GOARCH == "amd64" && kvmUsableAt("/dev/kvm")
}

func kvmUsableAt(path string) bool {
    f, err := os.OpenFile(path, os.O_RDWR, 0)
    if err != nil { return false }
    defer f.Close()
    st, err := f.Stat()
    if err != nil || st.Mode() & os.ModeCharDevice == 0 { return false }
    version, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), kvmGetAPIVersion, 0)
    return errno == 0 && version == 12
}
