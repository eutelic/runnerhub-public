package agentupdategate

import (
	"context"
	"fmt"
	"github.com/eutelic/runnerhub/internal/agentupdate"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/scheduler"
	"github.com/eutelic/runnerhub/internal/store"
	"time"
)

// Gate re-reads persisted state on every check. It must be used together
// with the durable per-host lease and a privileged Docker topology check.
// A dashboard preflight snapshot alone is never a safe swap authorization.
type Gate struct {
	Store           store.Store
	Now             func() time.Time
	MaxHeartbeatAge time.Duration
}

func (g Gate) CheckDrained(ctx context.Context, hostID string) error {
	if g.Store == nil || hostID == "" {
		return fmt.Errorf("%w: host or store missing", agentupdate.ErrUnsafe)
	}
	host, err := g.Store.GetHost(ctx, hostID)
	if err != nil {
		return fmt.Errorf("%w: cannot load exact host: %v", agentupdate.ErrUnsafe, err)
	}
	if host.Provider != "docker" || host.State != core.HostDraining {
		return fmt.Errorf("%w: host must be a draining Docker agent", agentupdate.ErrUnsafe)
	}
	now := time.Now().UTC()
	if g.Now != nil {
		now = g.Now().UTC()
	}
	maxAge := g.MaxHeartbeatAge
	if maxAge <= 0 || maxAge > time.Minute {
		maxAge = 45 * time.Second
	}
	if host.LastSeenAt.IsZero() || now.Sub(host.LastSeenAt) > maxAge || host.LastSeenAt.After(now.Add(15*time.Second)) {
		return fmt.Errorf("%w: host heartbeat is missing or stale", agentupdate.ErrUnsafe)
	}
	var workers []core.Worker
	if optimized, ok := g.Store.(store.NonDeletedWorkerReader); ok {
		workers, err = optimized.ListNonDeletedWorkers(ctx)
	} else {
		workers, err = g.Store.ListWorkers(ctx)
	}
	if err != nil {
		return fmt.Errorf("%w: cannot confirm worker cleanup: %v", agentupdate.ErrUnsafe, err)
	}
	for _, w := range workers {
		if w.HostID == hostID && scheduler.OccupiesHostResources(w) {
			return fmt.Errorf("%w: unfinished worker or provider cleanup reservation on selected host", agentupdate.ErrUnsafe)
		}
	}
	return nil
}
