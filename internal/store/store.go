package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
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

// ScaleWorkerReader is an optional indexed lookup for durable scale-set
// admissions. Deleted workers are intentionally included so redelivered JIT
// requests cannot re-create a historical runner identity.
type ScaleWorkerReader interface {
	GetScaleWorker(context.Context, string, string) (core.Worker, error)
}

// CIWorkerCandidateReader returns only workers that can contribute to recent CI findings.
type CIWorkerCandidateReader interface {
	ListCIWorkerCandidates(context.Context, time.Time, time.Time) ([]core.Worker, error)
}

// MaintenanceState gates new worker admissions while existing jobs drain.
type MaintenanceState struct {
	Paused      bool      `json:"paused"`
	Generation  string    `json:"generation,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	RequestedBy string    `json:"requestedBy,omitempty"`
	RequestedAt time.Time `json:"requestedAt,omitempty"`
}

// MaintenanceStore persists the global admission gate across restarts.
type MaintenanceStore interface {
	GetMaintenance(context.Context) (MaintenanceState, error)
	SetMaintenance(context.Context, MaintenanceState) error
}

// ProviderDeleteResultRecorder atomically records the first authenticated
// deletion command result and its audit event. Replayed callbacks return false.
// It is deliberately optional for store wrappers that do not persist agent results.
type ProviderDeleteResultRecorder interface {
	RecordProviderDeleteResult(context.Context, string, string, string, core.AuditEvent) (bool, error)
}

// ProviderDeleteSummary reports durable authenticated agent responses. These
// records describe provider Delete calls, not independent physical reclamation.
type ProviderDeleteSummary struct {
	Reports             int64      `json:"reports"`
	Failures            int64      `json:"failures"`
	Measured            int64      `json:"measured"`
	DurationTotalNanos  int64      `json:"durationTotalNanos"`
	FirstObservedAt     *time.Time `json:"firstObservedAt,omitempty"`
	LastObservedAt      *time.Time `json:"lastObservedAt,omitempty"`
	Scope               string     `json:"scope"`
	PhysicalReclamation bool       `json:"physicalReclamationVerified"`
}

// ProviderDeleteSummaryReader returns the durable coverage window for agent
// delete responses independently of the process-local telemetry counters.
type ProviderDeleteSummaryReader interface {
	ProviderDeleteSummary(context.Context) (ProviderDeleteSummary, error)
}

func providerDeleteDuration(detail string) int64 {
	for _, field := range strings.Fields(detail) {
		if value, ok := strings.CutPrefix(field, "agent_duration_ns="); ok {
			duration, err := strconv.ParseInt(value, 10, 64)
			if err == nil && duration > 0 && duration <= int64(24*time.Hour) {
				return duration
			}
			return 0
		}
	}
	return 0
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
