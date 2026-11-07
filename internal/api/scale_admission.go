package api

import (
	"context"
	"fmt"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

// findScaleWorker consults the durable ledger, including deleted workers. The
// lookup must run under Service.workerScheduleMu along with host selection,
// otherwise a redelivered scale-set request could reserve a second slot.
func (s *Service) findScaleWorker(ctx context.Context, poolID, runnerName string) (core.Worker, error) {
	if indexed, ok := s.Store.(store.ScaleWorkerReader); ok {
		return indexed.GetScaleWorker(ctx, poolID, runnerName)
	}
	workers, err := s.Store.ListWorkers(ctx)
	if err != nil {
		return core.Worker{}, err
	}
	var found core.Worker
	matches := 0
	for _, worker := range workers {
		if worker.PoolID == poolID && worker.RunnerName == runnerName {
			found = worker
			matches++
		}
	}
	if matches > 1 {
		return core.Worker{}, fmt.Errorf("ambiguous scale worker identity for pool %q runner %q", poolID, runnerName)
	}
	if matches == 0 {
		return core.Worker{}, store.ErrNotFound
	}
	return found, nil
}

// A runner name alone is not sufficient for retry authentication. The new
// registration must match the same immutable GitHub runner ID, repository,
// and workflow. Legacy requests without an ID fail closed on name collision.
func sameScaleWorkerIdentity(worker core.Worker, poolID, runnerName string, githubRunnerID int64, repository, workflow string) bool {
	return githubRunnerID > 0 &&
		worker.PoolID == poolID &&
		worker.RunnerName == runnerName &&
		worker.GitHubRunnerID == githubRunnerID &&
		worker.Repository == repository &&
		(workflow == "" || worker.WorkflowRef == workflow)
}
