package store

import (
	"context"
	"errors"
	"time"
)

// AgentUpdateLease stores only nonsecret control-plane metadata. Any running
// Docker configuration or credential material remains on the selected host.
type AgentUpdateLease struct {
	HostID           string    `json:"hostId"`
	OperationID      string    `json:"operationId"`
	PreviousRevision string    `json:"previousRevision,omitempty"`
	Actor            string    `json:"actor,omitempty"`
	StartedAt        time.Time `json:"startedAt"`
	TargetImage      string    `json:"targetImage"`
	TargetRevision   string    `json:"targetRevision"`
	HelperImage      string    `json:"helperImage,omitempty"`
	RecoveryOwner    string    `json:"recoveryOwner,omitempty"`
	State            string    `json:"state"`
	Step             string    `json:"step"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

var ErrAgentUpdateBusy = errors.New("host already has an unresolved agent update")
var ErrAgentUpdateChanged = errors.New("agent update operation changed concurrently")

type AgentUpdateLeaseStore interface {
	BeginAgentUpdateLease(context.Context, AgentUpdateLease) (AgentUpdateLease, bool, error)
	GetAgentUpdateLease(context.Context, string) (AgentUpdateLease, error)
	ListActiveAgentUpdateLeases(context.Context) ([]AgentUpdateLease, error)
	ClaimAgentUpdateRecovery(context.Context, string, string, string) (AgentUpdateLease, error)
	TransitionAgentUpdateLease(context.Context, string, string, string, string, string) error
	CompleteAgentUpdateLease(context.Context, string, string, string, ...string) (AgentUpdateLease, error)
}
