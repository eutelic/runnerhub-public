package ciaudit

import (
	"errors"
	"strings"
)

// ValidateFinding rejects incomplete or unsupported evidence before it is
// persisted. Analyzer versions are part of the durable finding identity, so
// callers must always provide one explicitly.
func ValidateFinding(f Finding) error {
	if strings.TrimSpace(f.JobID) == "" {
		return errors.New("CI finding job id is required")
	}
	if strings.TrimSpace(f.DetectorID) == "" {
		return errors.New("CI finding detector id is required")
	}
	if strings.TrimSpace(f.AnalysisVersion) == "" {
		return errors.New("CI finding analysis version is required")
	}
	switch f.Class {
	case EvidenceConfirmed, EvidenceInputEquivalent, EvidenceDependencyProven, EvidenceHighConfidence, EvidenceHeuristic, EvidenceUnknown:
	default:
		return errors.New("CI finding evidence class is invalid")
	}
	if f.Interval.Start.IsZero() || f.Interval.End.IsZero() || !f.Interval.Start.Before(f.Interval.End) {
		return errors.New("CI finding interval must have start before end")
	}
	return nil
}
