//go:build !linux

package hostinfo

// KVMUsable is Linux-only; never advertise acceleration on other hosts.
func KVMUsable() bool { return false }
