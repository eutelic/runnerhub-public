package api

import (
  "errors"
  "net/http"
  "time"

  "github.com/eutelic/runnerhub/internal/auth"
  "github.com/eutelic/runnerhub/internal/core"
  "github.com/eutelic/runnerhub/internal/store"
)

// POST /api/v1/hosts/{id}/archive retires one old host identity only.
// This does not delete historical workers or the audit ledger.
func (s *Service) archiveHost(w http.ResponseWriter, r *http.Request) {
  if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
    problem(w, http.StatusForbidden, errors.New("archiving a host requires admin access"))
    return
  }
  archiver, ok := s.Store.(store.HostArchiver)
  if !ok {
    problem(w, http.StatusNotImplemented, errors.New("safe host archival is unsupported by this storage backend"))
    return
  }
  var input struct { TargetHostID string `json:"targetHostId,omitempty"` }
  if !decode(w,r,&input) { return }
  host, err := archiver.ArchiveHost(r.Context(), r.PathValue("id"), input.TargetHostID, time.Now().UTC())
  if err != nil {
    switch {
    case errors.Is(err, store.ErrNotFound):
      problem(w, http.StatusNotFound, err)
    case errors.Is(err, store.ErrHostNotArchivable):
      problem(w, http.StatusConflict, err)
    default:
      problem(w, http.StatusInternalServerError, err)
    }
    return
  }
  _ = s.Store.AppendAudit(r.Context(), core.AuditEvent{
    Actor: actor(r.Context()), Action: "archive_host", ResourceType: "host",
    ResourceID: host.ID, Result: "ok", SafeDetail: "Retired offline host identity; worker history retained; merge target: " + host.MergedIntoHostID,
  })
  writeJSON(w, http.StatusOK, host)
}
