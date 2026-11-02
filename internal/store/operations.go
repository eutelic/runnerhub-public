package store

import (
	"context"
	"time"
)

// Operation contains safe, non-secret state for a long-running action.
// Never place passwords, SSH keys, enrollment tokens, or raw remote output here.
type Operation struct {
	ID             string           `json:"id"`
	Kind           string           `json:"kind"`
	Target         string           `json:"target"`
	Actor          string           `json:"actor"`
	State          string           `json:"state"`
	Step           string           `json:"step"`
	Detail         string           `json:"detail,omitempty"`
	HostID         string           `json:"hostId,omitempty"`
	StartedAt      time.Time        `json:"startedAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
	LastProgressAt time.Time        `json:"lastProgressAt"`
	EndedAt        *time.Time       `json:"endedAt,omitempty"`
	Events         []OperationEvent `json:"events"`
}

type OperationEvent struct {
	State string    `json:"state"`
	Step  string    `json:"step"`
	At    time.Time `json:"at"`
}

// OperationStore extends Store without imposing a new implementation on
// independently provided stores. Production SQLite implements this interface.
type OperationStore interface {
	StartOperation(context.Context, Operation) (Operation, bool, error)
	GetOperation(context.Context, string) (Operation, error)
	ListOperations(context.Context, int) ([]Operation, error)
	UpdateOperation(context.Context, Operation) error
}
