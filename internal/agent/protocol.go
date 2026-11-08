package agent

import (
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
	"time"
)

type EnrollRequest struct {
	Name         string                `json:"name"`
	Provider     string                `json:"provider"`
	Capabilities provider.Capabilities `json:"capabilities"`
	AgentVersion string                `json:"agentVersion"`
	PlacementDomainID string `json:"placementDomainId,omitempty"`
}
type EnrollResponse struct {
	HostID              string `json:"hostId"`
	AgentToken          string `json:"agentToken"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds"`
}
type Heartbeat struct {
	HostID       string                          `json:"hostId"`
	At           time.Time                       `json:"at"`
	AgentVersion string                          `json:"agentVersion,omitempty"`
	Capabilities provider.Capabilities           `json:"capabilities"`
	Active       map[string]provider.WorkerState `json:"active,omitempty"`
}
type CommandType string

const (
	CommandCreate  CommandType = "create"
	CommandInspect CommandType = "inspect"
	CommandStop    CommandType = "stop"
	CommandDelete  CommandType = "delete"
	CommandUpdateAgent CommandType = "update-agent"
)

// AgentUpdateSpec is never an arbitrary Docker execution request. It is
// generated only from a durable operation targeting the current host ID.
// The host launcher must independently verify both pinned official images.
type AgentUpdateSpec struct {
 HostID string `json:"hostId"`
 OperationID string `json:"operationId"`
 TargetImage string `json:"targetImage"`
 TargetRevision string `json:"targetRevision"`
 HelperImage string `json:"helperImage"`
}

type Command struct {
	ID        string                 `json:"id"`
	Type      CommandType            `json:"type"`
	WorkerID  string                 `json:"workerId"`
	Provider  string                 `json:"provider"`
	Spec      *provider.WorkerSpec   `json:"spec,omitempty"`
	Handle    *provider.WorkerHandle `json:"handle,omitempty"`
	AgentUpdate *AgentUpdateSpec `json:"agentUpdate,omitempty"`
	CreatedAt time.Time              `json:"createdAt"`
}
type CommandResult struct {
	CommandID string                 `json:"commandId"`
	WorkerID  string                 `json:"workerId"`
	OK        bool                   `json:"ok"`
	Handle    *provider.WorkerHandle `json:"handle,omitempty"`
	State     *provider.WorkerState  `json:"state,omitempty"`
// ProviderDeleteDurationNanos measures the agent-side provider Delete call only.
	// Zero means unavailable (for example, an older agent); it does not imply success.
	ProviderDeleteDurationNanos int64 `json:"providerDeleteDurationNanos,omitempty"`
	SafeError string                 `json:"safeError,omitempty"`
	At        time.Time              `json:"at"`
}
type HostView struct {
	Host         core.Host             `json:"host"`
	Capabilities provider.Capabilities `json:"capabilities"`
}
