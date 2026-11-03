//go:build !localtest

package store

import (
  "context"
  "database/sql"
  "encoding/json"
  "errors"
  "fmt"
  "time"

  "github.com/eutelic/runnerhub/internal/core"
)

// ArchiveHost checks every blocker and persists the archived state within one transaction.
// It does not delete historical hosts or jobs.
func (s *SQLite) ArchiveHost(ctx context.Context, id, targetID string, now time.Time) (core.Host, error) {
  tx, err := s.db.BeginTx(ctx, nil)
  if err != nil { return core.Host{}, err }
  defer tx.Rollback()

  var payload []byte
  if err := tx.QueryRowContext(ctx, "SELECT data FROM hosts WHERE id=?", id).Scan(&payload); err != nil {
    if errors.Is(err, sql.ErrNoRows) { return core.Host{}, ErrNotFound }
    return core.Host{}, err
  }
  var host core.Host
  if err := json.Unmarshal(payload, &host); err != nil { return core.Host{}, err }
  if host.State == core.HostArchived {
    if targetID != host.MergedIntoHostID { return core.Host{}, fmt.Errorf("%w: archived identity cannot be re-merged into another host", ErrHostNotArchivable) }
    return host, nil
  }
  if err := validateHostArchive(host, now); err != nil { return core.Host{}, err }
  if targetID != "" {
    var raw []byte
    if err := tx.QueryRowContext(ctx, "SELECT data FROM hosts WHERE id=?", targetID).Scan(&raw); err != nil {
      if errors.Is(err, sql.ErrNoRows) { return core.Host{}, fmt.Errorf("%w: merge target not found", ErrHostNotArchivable) }
      return core.Host{}, err
    }
    var target core.Host
    if err := json.Unmarshal(raw, &target); err != nil { return core.Host{}, err }
    if err := validateMergeTarget(host, target, now); err != nil { return core.Host{}, err }
  }

  rows, err := tx.QueryContext(ctx, "SELECT data FROM workers")
  if err != nil { return core.Host{}, err }
  for rows.Next() {
    var raw []byte
    if err := rows.Scan(&raw); err != nil { rows.Close(); return core.Host{}, err }
    var worker core.Worker
    if err := json.Unmarshal(raw, &worker); err != nil { rows.Close(); return core.Host{}, err }
    if worker.HostID == id && worker.State != core.WorkerDeleted {
      rows.Close()
      return core.Host{}, fmt.Errorf("%w: worker %s has not reached deleted state (%s)", ErrHostNotArchivable, worker.ID, worker.State)
    }
  }
  if err := rows.Err(); err != nil { rows.Close(); return core.Host{}, err }
  if err := rows.Close(); err != nil { return core.Host{}, err }

  var pending int
  if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_commands WHERE host_id=? AND acked_at IS NULL", id).Scan(&pending); err != nil {
    return core.Host{}, err
  }
  if pending > 0 {
    return core.Host{}, fmt.Errorf("%w: %d unacknowledged agent commands remain", ErrHostNotArchivable, pending)
  }

  host.State = core.HostArchived
  host.ArchivedAt = now.UTC()
  host.MergedIntoHostID = targetID
  payload, err = json.Marshal(host)
  if err != nil { return core.Host{}, err }
  if _, err := tx.ExecContext(ctx, "UPDATE hosts SET data=?,updated_at=? WHERE id=?", payload, now.UTC().Format(time.RFC3339Nano), id); err != nil {
    return core.Host{}, err
  }
  if err := tx.Commit(); err != nil { return core.Host{}, err }
  return host, nil
}
