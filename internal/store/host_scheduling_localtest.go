//go:build localtest

package store

import (
	"context"

	"github.com/eutelic/runnerhub/internal/core"
)

// SetHostSchedulingDisabled mirrors the production atomic mutation in the
// in-memory localtest store, under its existing mutex.
func (s *SQLite) SetHostSchedulingDisabled(_ context.Context, id string, disabled bool) (core.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	host, ok := s.hosts[id]
	if !ok {
		return core.Host{}, ErrNotFound
	}
	if host.State == core.HostArchived {
		return core.Host{}, ErrHostArchived
	}
	host.SchedulingDisabled = disabled
	s.hosts[id] = host
	return host, nil
}
