//go:build localtest

package store

import (
  "context"
  "fmt"
  "time"

  "github.com/eutelic/runnerhub/internal/core"
)

func (s *SQLite) ArchiveHost(_ context.Context, id, targetID string, now time.Time) (core.Host, error) {
  s.mu.Lock()
  defer s.mu.Unlock()
  host, ok := s.hosts[id]
  if !ok { return core.Host{}, ErrNotFound }
  if host.State == core.HostArchived {
    if targetID != host.MergedIntoHostID { return core.Host{}, fmt.Errorf("%w: archived identity cannot be re-merged", ErrHostNotArchivable) }
    return host, nil
  }
  if err := validateHostArchive(host, now); err != nil { return core.Host{}, err }
  if targetID != "" {
    target, ok := s.hosts[targetID]
    if !ok { return core.Host{}, fmt.Errorf("%w: merge target not found", ErrHostNotArchivable) }
    if err := validateMergeTarget(host, target, now); err != nil { return core.Host{}, err }
  }
  for _, worker := range s.workers {
    if worker.HostID == id && worker.State != core.WorkerDeleted {
      return core.Host{}, fmt.Errorf("%w: worker %s has not reached deleted state (%s)", ErrHostNotArchivable, worker.ID, worker.State)
    }
  }
  for _, cmd := range s.commands {
    if cmd.HostID == id && cmd.AckedAt.IsZero() {
      return core.Host{}, fmt.Errorf("%w: unacknowledged agent commands remain", ErrHostNotArchivable)
    }
  }
  host.State = core.HostArchived
  host.ArchivedAt = now.UTC()
  host.MergedIntoHostID = targetID
  s.hosts[id] = host
  return host, nil
}
