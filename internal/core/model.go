package core

import "time"

type HostState string

const (
	HostEnrolling HostState = "enrolling"
	HostOnline    HostState = "online"
	HostDraining  HostState = "draining"
	HostOffline   HostState = "offline"
	HostError     HostState = "error"
)

type WorkerState string

const (
	WorkerRequested      WorkerState = "requested"
	WorkerProvisioning   WorkerState = "provisioning"
	WorkerStarting       WorkerState = "starting"
	WorkerRegistering    WorkerState = "registering"
	WorkerReady          WorkerState = "ready"
	WorkerBusy           WorkerState = "busy"
	WorkerCompleted      WorkerState = "completed"
	WorkerDeleting       WorkerState = "deleting"
	WorkerDeleted        WorkerState = "deleted"
	WorkerFailed         WorkerState = "failed"
	WorkerCleanupPending WorkerState = "cleanup_pending"
)

type IsolationMode string

const (
	IsolationContainerTrusted IsolationMode = "container-trusted"
	IsolationHostPersistent   IsolationMode = "host-persistent"
	IsolationMicroVM          IsolationMode = "microvm"
	IsolationVMProvider       IsolationMode = "vm-provider"
)

type ScopeKind string

const (
	ScopeRepository   ScopeKind = "repository"
	ScopeOrganization ScopeKind = "organization"
)

type GitHubScope struct {
	Kind       ScopeKind `json:"kind"`
	Owner      string    `json:"owner"`
	Repository string    `json:"repository,omitempty"`
}

type Resources struct {
	CPUMillis   int64 `json:"cpuMillis,omitempty"`
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
	DiskBytes   int64 `json:"diskBytes,omitempty"`
	GPUs        int   `json:"gpus,omitempty"`
}

type Host struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Provider     string            `json:"provider"`
	State        HostState         `json:"state"`
	OS           string            `json:"os,omitempty"`
	Architecture string            `json:"architecture,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	Capacity     Resources         `json:"capacity"`
	AgentVersion string            `json:"agentVersion,omitempty"`
	LastSeenAt   time.Time         `json:"lastSeenAt,omitempty"`
}

type RunnerPool struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Scope          GitHubScope       `json:"scope"`
	Labels         []string          `json:"labels"`
	HostSelector   map[string]string `json:"hostSelector,omitempty"`
	Isolation      IsolationMode     `json:"isolation"`
	Resources      Resources         `json:"resources"`
	MinIdle        int               `json:"minIdle"`
	MaxConcurrency int               `json:"maxConcurrency"`
}

type Worker struct {
	ID             string      `json:"id"`
	HostID         string      `json:"hostId"`
	PoolID         string      `json:"poolId"`
	RunnerName     string      `json:"runnerName"`
	GitHubRunnerID int64       `json:"githubRunnerId,omitempty"`
	State          WorkerState `json:"state"`
	FailureReason  string      `json:"failureReason,omitempty"`
	CreatedAt      time.Time   `json:"createdAt"`
	UpdatedAt      time.Time   `json:"updatedAt"`
}
