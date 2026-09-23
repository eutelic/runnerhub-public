//go:build !localtest

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/ciaudit"
)

func normalizeCIFindingRepository(repository string) string {
	return strings.ToLower(strings.TrimSpace(repository))
}

func (s *SQLite) ensureCIFindingSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS ci_findings(
	repository TEXT NOT NULL,
	job_id TEXT NOT NULL,
	detector_id TEXT NOT NULL,
	analysis_version TEXT NOT NULL,
	class TEXT NOT NULL,
	interval_start_ns INTEGER NOT NULL,
	interval_end_ns INTEGER NOT NULL,
	evidence BLOB NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(repository, job_id, detector_id, analysis_version)
);
CREATE INDEX IF NOT EXISTS idx_ci_findings_repository_interval
	ON ci_findings(repository, interval_end_ns, interval_start_ns);
`)
	return err
}

func (s *SQLite) UpsertCIFinding(ctx context.Context, repository string, finding ciaudit.Finding) error {
	repository = normalizeCIFindingRepository(repository)
	if repository == "" {
		return errors.New("CI finding repository is required")
	}
	if err := ciaudit.ValidateFinding(finding); err != nil {
		return err
	}
	if err := s.ensureCIFindingSchema(ctx); err != nil {
		return err
	}
	evidence, err := json.Marshal(finding.Evidence)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO ci_findings(
	repository, job_id, detector_id, analysis_version, class,
	interval_start_ns, interval_end_ns, evidence, updated_at
) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(repository, job_id, detector_id, analysis_version) DO UPDATE SET
	class=excluded.class,
	interval_start_ns=excluded.interval_start_ns,
	interval_end_ns=excluded.interval_end_ns,
	evidence=excluded.evidence,
	updated_at=excluded.updated_at
`,
		repository,
		finding.JobID,
		finding.DetectorID,
		finding.AnalysisVersion,
		string(finding.Class),
		finding.Interval.Start.UnixNano(),
		finding.Interval.End.UnixNano(),
		evidence,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLite) ListCIFindings(ctx context.Context, repository string, start, end time.Time) ([]ciaudit.Finding, error) {
	repository = normalizeCIFindingRepository(repository)
	if repository == "" {
		return nil, errors.New("CI finding repository is required")
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return nil, errors.New("CI finding window must have start before end")
	}
	if err := s.ensureCIFindingSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT job_id, detector_id, analysis_version, class, interval_start_ns, interval_end_ns, evidence
FROM ci_findings
WHERE repository=? AND interval_end_ns>? AND interval_start_ns<?
ORDER BY interval_start_ns, job_id, detector_id
`,
		repository,
		start.UnixNano(),
		end.UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var findings []ciaudit.Finding
	for rows.Next() {
		var finding ciaudit.Finding
		var class string
		var intervalStartNS, intervalEndNS int64
		var evidence []byte
		if err := rows.Scan(
			&finding.JobID,
			&finding.DetectorID,
			&finding.AnalysisVersion,
			&class,
			&intervalStartNS,
			&intervalEndNS,
			&evidence,
		); err != nil {
			return nil, err
		}
		finding.Class = ciaudit.EvidenceClass(class)
		finding.Interval.Start = time.Unix(0, intervalStartNS).UTC()
		finding.Interval.End = time.Unix(0, intervalEndNS).UTC()
		if len(evidence) > 0 {
			if err := json.Unmarshal(evidence, &finding.Evidence); err != nil {
				return nil, err
			}
		}
		if err := ciaudit.ValidateFinding(finding); err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
}
