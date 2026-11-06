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

    maintenance, err := s.maintenanceState(r.Context())
    if err != nil { problem(w, http.StatusServiceUnavailable, err); return }
    now := time.Now().UTC()
    result := make([]fleet.Health, 0, len(pools))
    for _, pool := range pools {
        health := fleet.PoolHealth(pool, hosts, workers, now)
        if maintenance.Paused {
            health.Healthy = false
            health.Reasons = append(health.Reasons, "RunnerHub maintenance: new CI admissions paused for self-redeployment")
        }
        result = append(result, health)
    }
    writeJSON(w, http.StatusOK, result)
}
