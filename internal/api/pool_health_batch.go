package api

import (
    "net/http"
    "time"

    "github.com/eutelic/runnerhub/internal/core"
    "github.com/eutelic/runnerhub/internal/fleet"
    "github.com/eutelic/runnerhub/internal/store"
)

// poolHealthAll shares one live-worker snapshot across all pool diagnoses.
// Historical runtime state cannot establish current host eligibility.
func (s *Service) poolHealthAll(w http.ResponseWriter, r *http.Request) {
    pools, err := s.Store.ListPools(r.Context())
    if err != nil { problem(w, http.StatusInternalServerError, err); return }
    hosts, err := s.Store.ListHosts(r.Context())
    if err != nil { problem(w, http.StatusInternalServerError, err); return }

    var workers []core.Worker
    if live, ok := s.Store.(store.NonDeletedWorkerReader); ok {
        workers, err = live.ListNonDeletedWorkers(r.Context())
    } else {
        workers, err = s.Store.ListWorkers(r.Context())
    }
    if err != nil { problem(w, http.StatusInternalServerError, err); return }

    now := time.Now().UTC()
    result := make([]fleet.Health, 0, len(pools))
    for _, pool := range pools {
        result = append(result, fleet.PoolHealth(pool, hosts, workers, now))
    }
    writeJSON(w, http.StatusOK, result)
}
