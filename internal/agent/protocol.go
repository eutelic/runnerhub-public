package agent

import (
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/provider"
	"time"
)

type EnrollRequest struct {
	Name         string                `json:"name"`
	Provider     string                `json:"provider"`
	Capabilities provider.Capabilities `json:"capabilities"`
	AgentVersion string                `json:"agentVersion"`
}
type EnrollResponse struct {
	HostID              string `json:"hostId"`
	AgentToken          string `json:"agentToken"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds"`
}
type Heartbeat struct {
	HostID       string                          `json:"hostId"`
	At           time.Time                       `json:"at"`
	Capabilities provider.Capabilities           `json:"capabilities"`
	Active       map[string]provider.WorkerState `json:"active,omitempty"`
}
type CommandType string

const (
	CommandCreate  CommandType = "create"
	CommandInspect CommandType = "inspect"
	CommandStop    CommandType = "stop"
	CommandDelete  CommandType = "delete"
)

type Command struct {
	ID        string                 `json:"id"`
	Type      CommandType            `json:"type"`
	WorkerID  string                 `json:"workerId"`
	Provider  string                 `json:"provider"`
	Spec      *provider.WorkerSpec   `json:"spec,omitempty"`
	Handle    *provider.WorkerHandle `json:"handle,omitempty"`
	CreatedAt time.Time              `json:"createdAt"`
}
type CommandResult struct {
	CommandID string                 `json:"commandId"`
	WorkerID  string                 `json:"workerId"`
	OK        bool                   `json:"ok"`
	Handle    *provider.WorkerHandle `json:"handle,omitempty"`
	State     *provider.WorkerState  `json:"state,omitempty"`
	SafeError string                 `json:"safeError,omitempty"`
	At        time.Time              `json:"at"`
}
type HostView struct {
	Host         core.Host             `json:"host"`
	Capabilities provider.Capabilities `json:"capabilities"`
}
