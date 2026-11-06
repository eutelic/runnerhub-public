package store

import (
	"context"
	"errors"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/core"
)

var ErrNotFound = errors.New("store: not found")

type CommandRecord struct {
	HostID     string
	Command    agent.Command
	LeaseUntil time.Time
	Attempts   int
	AckedAt    time.Time
}

// NonDeletedWorkerReader is an optional optimized live-worker snapshot.
// The full Store.ListWorkers ledger remains available for history and CI analytics.
type NonDeletedWorkerReader interface {
	ListNonDeletedWorkers(context.Context) ([]core.Worker, error)
}

// CIWorkerCandidateReader returns only workers that can contribute to recent CI findings.
type CIWorkerCandidateReader interface {
	ListCIWorkerCandidates(context.Context, time.Time, time.Time) ([]core.Worker, error)
}

// MaintenanceState gates new worker admissions while existing jobs drain.
type MaintenanceState struct {
 Paused bool `json:"paused"`
 Generation string `json:"generation,omitempty"`
 Reason string `json:"reason,omitempty"`
 RequestedBy string `json:"requestedBy,omitempty"`
 RequestedAt time.Time `json:"requestedAt,omitempty"`
}

// MaintenanceStore persists the global admission gate across restarts.
type MaintenanceStore interface {
 GetMaintenance(context.Context) (MaintenanceState,error)
 SetMaintenance(context.Context,MaintenanceState) error
}

type Store interface {
	UpsertHost(context.Context, core.Host) error
	GetHost(context.Context, string) (core.Host, error)
	ListHosts(context.Context) ([]core.Host, error)
	UpsertPool(context.Context, core.RunnerPool) error
	GetPool(context.Context, string) (core.RunnerPool, error)
	ListPools(context.Context) ([]core.RunnerPool, error)
	UpsertWorker(context.Context, core.Worker) error
	GetWorker(context.Context, string) (core.Worker, error)
	ListWorkers(context.Context) ([]core.Worker, error)
	AppendAudit(context.Context, core.AuditEvent) error
	ListAudit(context.Context, int) ([]core.AuditEvent, error)

	EnqueueCommand(context.Context, string, agent.Command) error
	LeaseCommands(context.Context, string, time.Time, time.Time, int) ([]agent.Command, error)
	GetCommand(context.Context, string) (CommandRecord, error)
	AckCommand(context.Context, string, time.Time) error

	Close() error
}
