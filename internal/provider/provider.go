package provider

import (
	"context"
	"github.com/eutelic/runnerhub/internal/core"
)

type Capabilities struct {
	Provider string `json:"provider"`
	OS string `json:"os"`
	Architecture string `json:"architecture"`
	Capacity core.Resources `json:"capacity"`
	IsolationModes []core.IsolationMode `json:"isolationModes"`
	Features map[string]bool `json:"features,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

type WorkerSpec struct {
	WorkerID string `json:"workerId"`
	PoolID string `json:"poolId"`
	RunnerName string `json:"runnerName"`
	JITConfig string `json:"jitConfig"`
	Labels []string `json:"labels"`
	Isolation core.IsolationMode `json:"isolation"`
	Resources core.Resources `json:"resources"`
	Image string `json:"image,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type WorkerHandle struct {
	ProviderID string `json:"providerId"`
	HostID string `json:"hostId"`
}

type WorkerUsage struct {
	CPUMillis int64 `json:"cpuMillis,omitempty"`
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
	EnvelopeCPUMillis int64 `json:"envelopeCpuMillis,omitempty"`
	EnvelopeMemoryBytes int64 `json:"envelopeMemoryBytes,omitempty"`
}

type WorkerState struct {
	State core.WorkerState `json:"state"`
	ExitCode *int `json:"exitCode,omitempty"`
	OOMKilled bool `json:"oomKilled,omitempty"`
	SafeDetail string `json:"safeDetail,omitempty"`
	Diagnostics string `json:"diagnostics,omitempty"`
	Usage *WorkerUsage `json:"usage,omitempty"`
}

type Provider interface {
	Capabilities(context.Context)(Capabilities,error)
	Validate(context.Context,WorkerSpec) error
	Create(context.Context,WorkerSpec)(WorkerHandle,error)
	Inspect(context.Context,WorkerHandle)(WorkerState,error)
	Stop(context.Context,WorkerHandle) error
	Delete(context.Context,WorkerHandle) error
}
