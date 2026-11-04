//go:build !localtest

package store

import (
	"context"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

const ciWorkerTerminalAtExpr = "CASE WHEN COALESCE(json_extract(data,'$.completedAt'),'') NOT IN ('','0001-01-01T00:00:00Z') THEN json_extract(data,'$.completedAt') ELSE json_extract(data,'$.failedAt') END"

const ciWorkerCandidatesQuery = `SELECT data FROM workers
WHERE COALESCE(json_extract(data,'$.repository'),'') != ''
  AND CAST(COALESCE(json_extract(data,'$.workflowRunId'),0) AS INTEGER) > 0
  AND COALESCE(json_extract(data,'$.startedAt'),'') != ''
  AND ` + ciWorkerTerminalAtExpr + ` != ''
  AND julianday(` + ciWorkerTerminalAtExpr + `) > julianday(?)
  AND julianday(` + ciWorkerTerminalAtExpr + `) > julianday(json_extract(data,'$.startedAt'))
  AND julianday(json_extract(data,'$.startedAt')) <= julianday(?)
ORDER BY julianday(` + ciWorkerTerminalAtExpr + `), id`

// ListCIWorkerCandidates filters the full worker ledger in SQLite while
// retaining recent deleted workers that still contribute to CI audit findings.
func (s *SQLite) ListCIWorkerCandidates(ctx context.Context, cutoff, now time.Time) ([]core.Worker, error) {
	rows, err := s.db.QueryContext(ctx, ciWorkerCandidatesQuery, cutoff.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []core.Worker{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		worker, err := decode[core.Worker](raw)
		if err != nil {
			return nil, err
		}
		out = append(out, worker)
	}
	return out, rows.Err()
}
