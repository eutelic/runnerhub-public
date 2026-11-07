//go:build !localtest

package store

import (
	"context"
	"fmt"

	"github.com/eutelic/runnerhub/internal/core"
)

// GetScaleWorker looks up a durable GitHub JIT runner identity by its pool and
// exact runner name, including deleted worker history. An ambiguous historical
// duplicate is an error, never permission to admit another worker.
func (s *SQLite) GetScaleWorker(ctx context.Context, poolID, runnerName string) (core.Worker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM workers
		WHERE json_extract(data, '$.poolId') = ?
		  AND json_extract(data, '$.runnerName') = ?
		  AND COALESCE(json_extract(data, '$.runnerName'), '') != ''
		ORDER BY updated_at DESC, id DESC LIMIT 2`, poolID, runnerName)
	if err != nil {
		return core.Worker{}, err
	}
	defer rows.Close()

	var found core.Worker
	count := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return core.Worker{}, err
		}
		worker, err := decode[core.Worker](raw)
		if err != nil {
			return core.Worker{}, err
		}
		found = worker
		count++
	}
	if err := rows.Err(); err != nil {
		return core.Worker{}, err
	}
	switch count {
	case 0:
		return core.Worker{}, ErrNotFound
	case 1:
		return found, nil
	default:
		return core.Worker{}, fmt.Errorf("ambiguous scale worker identity for pool %q runner %q", poolID, runnerName)
	}
}
