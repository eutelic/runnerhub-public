//go:build !localtest

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (s *SQLite) ensureCIWorkflowRunQuarantineSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS ci_workflow_run_quarantines(
	repository TEXT NOT NULL,
	run_id INTEGER NOT NULL,
	data BLOB NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(repository, run_id)
);
`)
	return err
}

func (s *SQLite) GetCIWorkflowRunQuarantine(ctx context.Context, repository string, runID int64) (CIWorkflowRunQuarantine, error) {
	repository = normalizeCIRepository(repository)
	if repository == "" || runID <= 0 {
		return CIWorkflowRunQuarantine{}, errors.New("CI workflow run quarantine requires a repository and positive run id")
	}
	if err := s.ensureCIWorkflowRunQuarantineSchema(ctx); err != nil {
		return CIWorkflowRunQuarantine{}, err
	}
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM ci_workflow_run_quarantines WHERE repository=? AND run_id=?`,
		repository, runID,
	).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return CIWorkflowRunQuarantine{}, ErrNotFound
	}
	if err != nil {
		return CIWorkflowRunQuarantine{}, err
	}
	var record CIWorkflowRunQuarantine
	if err := json.Unmarshal(data, &record); err != nil {
		return CIWorkflowRunQuarantine{}, err
	}
	return record, nil
}

func (s *SQLite) UpsertCIWorkflowRunQuarantine(ctx context.Context, record CIWorkflowRunQuarantine) error {
	record.Repository = normalizeCIRepository(record.Repository)
	if record.Repository == "" || record.RunID <= 0 || record.FailureCount <= 0 ||
		record.Reason == "" || record.LastFailureAt.IsZero() || !record.RetryAfter.After(record.LastFailureAt) {
		return errors.New("CI workflow run quarantine requires repository, run id, reason, failure time, and future retry time")
	}
	if err := s.ensureCIWorkflowRunQuarantineSchema(ctx); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO ci_workflow_run_quarantines(repository, run_id, data, updated_at)
VALUES(?,?,?,?)
ON CONFLICT(repository, run_id) DO UPDATE SET
	data=excluded.data,
	updated_at=excluded.updated_at
`, record.Repository, record.RunID, data, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *SQLite) DeleteCIWorkflowRunQuarantine(ctx context.Context, repository string, runID int64) error {
	repository = normalizeCIRepository(repository)
	if repository == "" || runID < 0 {
		return errors.New("CI workflow run quarantine requires repository and positive run id")
	}
	if err := s.ensureCIWorkflowRunQuarantineSchema(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM ci_workflow_run_quarantines WHERE repository=? AND run_id=?`,
		repository, runID,
	)
	return err
}
