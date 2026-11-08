package core

import (
	"strings"
	"testing"
)

func TestAndroidEmulatorProfileFailsClosed(t *testing.T) {
	p := RunnerPool{
		Name:   "mobile-app-android-emulator",
		Scope:  GitHubScope{Kind: ScopeRepository, Owner: "eutelic", Repository: "mobile-app"},
		Labels: []string{AndroidEmulatorScaleSet}, ScaleSetName: AndroidEmulatorScaleSet,
		HostSelector: map[string]string{"android-emulator": "true", AndroidEmulatorCapabilityTag: "true"},
		Isolation:    IsolationHostPersistent, MaxConcurrency: 1, RunnerGroupID: 1,
		Resources: Resources{CPUMillis: 4000, MemoryBytes: 8 << 30, DiskBytes: 20 << 30},
	}
	if err := ValidateRunnerPool(p); err != nil {
		t.Fatalf("valid dedicated native profile: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*RunnerPool)
		want   string
	}{
		{"Docker isolation", func(x *RunnerPool) { x.Isolation = IsolationContainerTrusted }, "host-persistent"},
		{"missing device selector", func(x *RunnerPool) { delete(x.HostSelector, AndroidEmulatorCapabilityTag) }, "hardware.kvm"},
		{"missing dedicated tag", func(x *RunnerPool) { delete(x.HostSelector, "android-emulator") }, "android-emulator"},
		{"not enough CPU", func(x *RunnerPool) { x.Resources.CPUMillis = 2500 }, "4 CPU"},
		{"not enough memory", func(x *RunnerPool) { x.Resources.MemoryBytes = 4 << 30 }, "8 GiB"},
		{"not enough disk", func(x *RunnerPool) { x.Resources.DiskBytes = 10 << 30 }, "20 GiB"},
		{"concurrent process workers", func(x *RunnerPool) { x.MaxConcurrency = 2 }, "max concurrency 1"},
		{"resource auto tune", func(x *RunnerPool) { x.AutoTuneResources = true }, "auto-size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := p
			candidate.HostSelector = map[string]string{"android-emulator": "true", AndroidEmulatorCapabilityTag: "true"}
			tt.mutate(&candidate)
			if err := ValidateRunnerPool(candidate); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateRunnerPool=%v want %q", err, tt.want)
			}
		})
	}
	ordinary := p
	ordinary.ScaleSetName = "runnerhub"
	ordinary.Labels = []string{"runnerhub"}
	ordinary.Isolation = IsolationContainerTrusted
	ordinary.HostSelector = nil
	ordinary.MaxConcurrency = 2
	ordinary.Resources = Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}
	if err := ValidateRunnerPool(ordinary); err != nil {
		t.Fatalf("ordinary pool should stay unaffected: %v", err)
	}
}
