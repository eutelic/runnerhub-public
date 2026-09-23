//go:build !localtest

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/ciaudit"
)

func normalizeCIValidationRepository(repository string) string {
	return strings.ToLower(strings.TrimSpace(repository))
}

func (s *SQLite) ensureCIValidationCertificateSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS ci_validation_certificates(
	repository TEXT NOT NULL,
	validation_key TEXT NOT NULL,
	completed_at_ns INTEGER NOT NULL,
	data BLOB NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(repository, validation_key)
);
CREATE INDEX IF NOT EXISTS idx_ci_validation_certificates_repository_completed
	ON ci_validation_certificates(repository, completed_at_ns);
`)
	return err
}

func (s *SQLite) UpsertCIValidationCertificate(ctx context.Context, repository string, certificate ciaudit.ValidationCertificate) error {
	repository = normalizeCIValidationRepository(repository)
	if repository == "" {
		return errors.New("CI validation certificate repository is required")
	}
	if err := ciaudit.ValidateValidationCertificate(certificate); err != nil {
		return err
	}
	if err := s.ensureCIValidationCertificateSchema(ctx); err != nil {
		return err
	}
	data, err := json.Marshal(certificate)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO ci_validation_certificates(
	repository, validation_key, completed_at_ns, data, updated_at
) VALUES(?,?,?,?,?)
ON CONFLICT(repository, validation_key) DO UPDATE SET
	completed_at_ns=excluded.completed_at_ns,
	data=excluded.data,
	updated_at=excluded.updated_at
WHERE excluded.completed_at_ns < ci_validation_certificates.completed_at_ns
`,
		repository,
		certificate.Key,
		certificate.CompletedAt.UnixNano(),
		data,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLite) GetCIValidationCertificate(ctx context.Context, repository, key string) (ciaudit.ValidationCertificate, error) {
	repository = normalizeCIValidationRepository(repository)
	key = strings.TrimSpace(key)
	if repository == "" || key == "" {
		return ciaudit.ValidationCertificate{}, errors.New("CI validation certificate repository and key are required")
	}
	if err := s.ensureCIValidationCertificateSchema(ctx); err != nil {
		return ciaudit.ValidationCertificate{}, err
	}
	var data []byte
	err := s.db.QueryRowContext(ctx, `
SELECT data
FROM ci_validation_certificates
WHERE repository=? AND validation_key=?
`, repository, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return ciaudit.ValidationCertificate{}, ErrNotFound
	}
	if err != nil {
		return ciaudit.ValidationCertificate{}, err
	}
	var certificate ciaudit.ValidationCertificate
	if err := json.Unmarshal(data, &certificate); err != nil {
		return ciaudit.ValidationCertificate{}, err
	}
	if err := ciaudit.ValidateValidationCertificate(certificate); err != nil {
		return ciaudit.ValidationCertificate{}, err
	}
	return certificate, nil
}
