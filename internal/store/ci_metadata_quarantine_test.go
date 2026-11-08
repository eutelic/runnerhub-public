//go:build !localtest

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCIWorkflowRunQuarantineRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 11, 4, 20, 0, 0, 0, time.UTC)
	record := CIWorkflowRunQuarantine{
		Repository:    "Eutelic/RunnerHub",
		RunID:         37333906258,
		FailureCount:  2,
		Reason:        "permission_denied",
		LastFailureAt: now,
		RetryAfter:    now.Add(12 * time.Hour),
	}
	if err := st.UpsertCIWorkflowRunQuarantine(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCIWorkflowRunQuarantine(ctx, "eutelic/runnerhub", record.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repository != "eutelic/runnerhub" || got.FailureCount != 2 || got.Reason != record.Reason ||
		!got.LastFailureAt.Equal(now) || !got.RetryAfter.Equal(record.RetryAfter) {
		t.Fatalf("quarantine=%+v", got)
	}
	if err := st.DeleteCIWorkflowRunQuarantine(ctx, "EUTELIC/RUNNERHUB", record.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCIWorkflowRunQuarantine(ctx, "eutelic/runnerhub", record.RunID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted quarantine error=%v, want ErrNotFound", err)
	}

}
