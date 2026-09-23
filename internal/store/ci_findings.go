package store

import (
	"context"
	"time"

	"github.com/eutelic/runnerhub/internal/ciaudit"
)

// CIFindingStore is the durable evidence ledger used by the CI efficiency
// analyzer. It intentionally remains separate from Store so existing test and
// integration stores do not have to implement analytics persistence.
type CIFindingStore interface {
	UpsertCIFinding(context.Context, string, ciaudit.Finding) error
	ListCIFindings(context.Context, string, time.Time, time.Time) ([]ciaudit.Finding, error)
}
