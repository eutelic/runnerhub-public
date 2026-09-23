//go:build !localtest

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func normalizeCIRepository(repository string) string {
	return strings.ToLower(strings.TrimSpace(repository))
}

func (s *SQLite) ensureCIMetadataSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS ci_workflow_runs(
	repository TEXT NOT NULL,
	run_id INTEGER NOT NULL,
	attempt INTEGER NOT NULL,
	created_at_ns INTEGER NOT NULL,
	data BLOB NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(repository, run_id, attempt)
);
CREATE INDEX IF NOT EXISTS idx_ci_workflow_runs_repository_created
	ON ci_workflow_runs(repository, created_at_ns);
CREATE TABLE IF NOT EXISTS ci_pull_requests(
	repository TEXT NOT NULL,
	number INTEGER NOT NULL,
	data BLOB NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(repository, number)
);
`)
	return err
}

func (s *SQLite) UpsertCIWorkflowRun(ctx context.Context, run CIWorkflowRun) error {
	run.Repository = normalizeCIRepository(run.Repository)
	if run.Repository == "" || run.RunID <= 0 || run.Attempt <= 0 || run.CreatedAt.IsZero() {
		return errors.New("CI workflow run requires repository, run id, attempt, and created time")
	}
	if err := s.ensureCIMetadataSchema(ctx); err != nil {
		return err
	}
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO ci_workflow_runs(repository, run_id, attempt, created_at_ns, data, updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(repository, run_id, attempt) DO UPDATE SET
	created_at_ns=excluded.created_at_ns,
	data=excluded.data,
	updated_at=excluded.updated_at
`,
		run.Repository,
		run.RunID,
		run.Attempt,
		run.CreatedAt.UnixNano(),
		data,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLite) ListCIWorkflowRuns(ctx context.Context, repository string, start, end time.Time) ([]CIWorkflowRun, error) {
	repository = normalizeCIRepository(repository)
	if repository == "" {
		return nil, errors.New("CI workflow run repository is required")
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return nil, errors.New("CI workflow run window must have start before end")
	}
	if err := s.ensureCIMetadataSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT data
FROM ci_workflow_runs
WHERE repository=? AND created_at_ns>=? AND created_at_ns<?
ORDER BY created_at_ns, run_id, attempt
`, repository, start.UnixNano(), end.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CIWorkflowRun
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var run CIWorkflowRun
		if err := json.Unmarshal(data, &run); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *SQLite) UpsertCIPullRequest(ctx context.Context, pr CIPullRequest) error {
	pr.Repository = normalizeCIRepository(pr.Repository)
	if pr.Repository == "" || pr.Number <= 0 {
		return errors.New("CI pull request requires repository and number")
	}
	if err := s.ensureCIMetadataSchema(ctx); err != nil {
		return err
	}
	data, err := json.Marshal(pr)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO ci_pull_requests(repository, number, data, updated_at)
VALUES(?,?,?,?)
ON CONFLICT(repository, number) DO UPDATE SET
	data=excluded.data,
	updated_at=excluded.updated_at
`, pr.Repository, pr.Number, data, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *SQLite) GetCIPullRequest(ctx context.Context, repository string, number int) (CIPullRequest, error) {
	repository = normalizeCIRepository(repository)
	if repository == "" || number <= 0 {
		return CIPullRequest{}, errors.New("CI pull request requires repository and positive number")
	}
	if err := s.ensureCIMetadataSchema(ctx); err != nil {
		return CIPullRequest{}, err
	}
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM ci_pull_requests WHERE repository=? AND number=?`, repository, number).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return CIPullRequest{}, ErrNotFound
	}
	if err != nil {
		return CIPullRequest{}, err
	}
	var pr CIPullRequest
	if err := json.Unmarshal(data, &pr); err != nil {
		return CIPullRequest{}, err
	}
	return pr, nil
}
