//go:build !localtest

package store

import (
	"context"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/ciaudit"
)

func TestCIFindingStoreRoundTripAndWindow(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 9, 22, 20, 0, 0, 0, time.UTC)
	finding := ciaudit.Finding{
		JobID:           "worker-1",
		DetectorID:      "post-merge-validation",
		Class:           ciaudit.EvidenceConfirmed,
		AnalysisVersion: ciaudit.AnalyzerVersion,
		Interval: ciaudit.Interval{
			Start: now.Add(-30 * time.Minute),
			End:   now.Add(-20 * time.Minute),
		},
		Evidence: []string{"pull request merged before validation completed"},
	}
	if err := st.UpsertCIFinding(context.Background(), "Eutelic/RunnerHub", finding); err != nil {
		t.Fatal(err)
	}

	finding.Evidence = []string{"updated deterministic evidence"}
	if err := st.UpsertCIFinding(context.Background(), "eutelic/runnerhub", finding); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListCIFindings(context.Background(), "EUTELIC/RUNNERHUB", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("findings=%d want 1", len(got))
	}
	if len(got[0].Evidence) != 1 || got[0].Evidence[0] != "updated deterministic evidence" {
		t.Fatalf("finding=%+v", got[0])
	}

	got, err = st.ListCIFindings(context.Background(), "eutelic/runnerhub", now.Add(-10*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("out-of-window findings=%d want 0", len(got))
	}
}
