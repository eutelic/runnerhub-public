package core

import (
	"errors"
	"strings"
)

const (
	AndroidEmulatorScaleSet      = "runnerhub-android-emulator"
	AndroidEmulatorCapabilityTag = "hardware.kvm"
	AndroidEmulatorDedicatedTag  = "workload.android-emulator-only"
)

// IsAndroidEmulatorPool makes the dedicated scale-set fail closed even when a
// pool is created outside the dashboard API.
func IsAndroidEmulatorPool(p RunnerPool) bool {
	if strings.EqualFold(strings.TrimSpace(p.ScaleSetName), AndroidEmulatorScaleSet) {
		return true
	}
	for _, label := range p.Labels {
		if strings.EqualFold(strings.TrimSpace(label), AndroidEmulatorScaleSet) {
			return true
		}
	}
	return false
}

func ValidateSpecializedPool(p RunnerPool) error {
	if !IsAndroidEmulatorPool(p) {
		return nil
	}
	if p.Isolation != IsolationHostPersistent {
		return errors.New("runnerhub-android-emulator requires host-persistent native isolation")
	}
	if p.Scope.Kind != ScopeRepository {
		return errors.New("runnerhub-android-emulator must be scoped to one trusted private repository")
	}
	if p.HostSelector["android-emulator"] != "true" || p.HostSelector[AndroidEmulatorCapabilityTag] != "true" {
		return errors.New("runnerhub-android-emulator must require android-emulator and hardware.kvm host tags")
	}
	if p.Resources.CPUMillis < 4000 || p.Resources.MemoryBytes < (8<<30) || p.Resources.DiskBytes < (20<<30) {
		return errors.New("runnerhub-android-emulator requires at least 4 CPU, 8 GiB RAM, and 20 GiB disk")
	}
	if p.MaxConcurrency != 1 {
		return errors.New("runnerhub-android-emulator requires max concurrency 1 on its dedicated native runner")
	}
	if p.AutoTuneResources {
		return errors.New("runnerhub-android-emulator cannot auto-size below verified emulator resource requirements")
	}
	return nil
}
