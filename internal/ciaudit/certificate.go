package ciaudit

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ValidateValidationCertificate rejects incomplete certificate records before
// they enter durable storage. The validation key itself is checked against a
// ValidationIdentity at reuse time because the analyzer version is encoded in
// the key rather than duplicated in the certificate.
func ValidateValidationCertificate(c ValidationCertificate) error {
	if strings.TrimSpace(c.Key) == "" {
		return errors.New("validation certificate key is required")
	}
	if strings.TrimSpace(c.Result) == "" {
		return errors.New("validation certificate result is required")
	}
	if c.CompletedAt.IsZero() {
		return errors.New("validation certificate completion time is required")
	}
	if strings.TrimSpace(c.DefinitionHash) == "" || strings.TrimSpace(c.InputHash) == "" || strings.TrimSpace(c.ContextHash) == "" {
		return errors.New("validation certificate requires definition, input, and context hashes")
	}
	switch c.Freshness {
	case FreshnessContentAddressed, FreshnessExternal, FreshnessLive:
	default:
		return errors.New("validation certificate freshness class is invalid")
	}
	return nil
}

// EquivalentValidationFinding reports a fully avoidable execution when a
// successful validation certificate for the exact same identity already
// existed before the execution started.
//
// Content-addressed certificates are reusable indefinitely. External-state
// certificates require a positive TTL supplied by the detector policy. Live
// validation is never reused.
func EquivalentValidationFinding(job JobExecution, identity ValidationIdentity, certificate ValidationCertificate, externalTTL time.Duration) (Finding, bool) {
	if !validJob(job) || ValidateValidationCertificate(certificate) != nil {
		return Finding{}, false
	}
	key, err := identity.Key()
	if err != nil || key != strings.TrimSpace(certificate.Key) {
		return Finding{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(certificate.Result), "success") {
		return Finding{}, false
	}
	if !certificate.CompletedAt.Before(job.StartedAt) {
		return Finding{}, false
	}

	switch certificate.Freshness {
	case FreshnessContentAddressed:
		// Stable until the validation identity changes.
	case FreshnessExternal:
		if externalTTL <= 0 || job.StartedAt.Sub(certificate.CompletedAt) > externalTTL {
			return Finding{}, false
		}
	case FreshnessLive:
		return Finding{}, false
	default:
		return Finding{}, false
	}

	return Finding{
		JobID:           job.ID,
		DetectorID:      "equivalent-validation",
		Class:           EvidenceInputEquivalent,
		AnalysisVersion: AnalyzerVersion,
		Interval:        Interval{Start: job.StartedAt, End: job.CompletedAt},
		Evidence: []string{
			fmt.Sprintf("successful validation certificate %s already existed", certificate.Key),
			fmt.Sprintf("certificate completed at %s before this execution started", certificate.CompletedAt.UTC().Format(time.RFC3339Nano)),
			fmt.Sprintf("freshness class %q was reusable at execution start", certificate.Freshness),
		},
	}, true
}
