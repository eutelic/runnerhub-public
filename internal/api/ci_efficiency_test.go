package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/ciaudit"
	"github.com/eutelic/runnerhub/internal/cireconcile"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestSummarizeCIEfficiencyUsesWindowAndRunnerClasses(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	pools := []core.RunnerPool{
		{ID: "normal", ScaleSetName: "runnerhub", Labels: []string{"runnerhub"}},
		{ID: "publisher", ScaleSetName: publisherScaleSetName, Labels: []string{publisherScaleSetName}},
	}
	workers := []core.Worker{
		{
			ID: "normal-job", PoolID: "normal", Repository: "eutelic/runnerhub",
			QueuedAt: now.Add(-2*time.Hour - 30*time.Second), StartedAt: now.Add(-2 * time.Hour), CompletedAt: now.Add(-time.Hour),
			Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 2 << 30},
			ValidationAnalysisVersion: ciaudit.AnalyzerVersion, ValidationKey: "validation-key",
		},
		{
			ID: "publisher-job", PoolID: "publisher", Repository: "eutelic/runnerhub",
			QueuedAt: now.Add(-95 * time.Minute), StartedAt: now.Add(-90 * time.Minute), CompletedAt: now.Add(-30 * time.Minute),
			Resources: core.Resources{CPUMillis: 2000, MemoryBytes: 4 << 30},
			ValidationExclusionAnalysisVersion: ciaudit.AnalyzerVersion, ValidationExclusionReason: "unsupported-isolation",
		},
		{
			ID: "window-edge", PoolID: "normal", Repository: "EUTELIC/RUNNERHUB",
			StartedAt: now.Add(-31 * 24 * time.Hour), CompletedAt: now.Add(-29 * 24 * time.Hour),
			Resources: core.Resources{CPUMillis: 500, MemoryBytes: 1 << 30},
		},
		{
			ID: "old", PoolID: "normal", Repository: "eutelic/runnerhub",
			StartedAt: now.Add(-41 * 24 * time.Hour), CompletedAt: now.Add(-40 * 24 * time.Hour),
			Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
		},
		{
			ID: "other-repo", PoolID: "normal", Repository: "eutelic/other",
			StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-30 * time.Minute),
			Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
		},
	}

	got := summarizeCIEfficiencyAt("eutelic/runnerhub", "30d", 30*24*time.Hour, now, workers, pools, nil, false)
	if got.Jobs != 3 {
		t.Fatalf("jobs=%d want 3", got.Jobs)
	}
	if got.Execution.RunnerSeconds != 93600 {
		t.Fatalf("runner seconds=%v want 93600", got.Execution.RunnerSeconds)
	}
	if got.Execution.AllocatedCPUSeconds != 54000 {
		t.Fatalf("allocated cpu seconds=%v want 54000", got.Execution.AllocatedCPUSeconds)
	}
	if got.Queue.Overall.Samples != 2 || got.Queue.Overall.P50Seconds == nil || *got.Queue.Overall.P50Seconds != 30 || got.Queue.Overall.P95Seconds == nil || *got.Queue.Overall.P95Seconds != 300 {
		t.Fatalf("overall queue=%+v", got.Queue.Overall)
	}
	if got.Queue.ByRunnerClass["runnerhub"].P50Seconds == nil || *got.Queue.ByRunnerClass["runnerhub"].P50Seconds != 30 {
		t.Fatalf("runnerhub queue=%+v", got.Queue.ByRunnerClass["runnerhub"])
	}
	if got.Queue.ByRunnerClass[publisherScaleSetName].P95Seconds == nil || *got.Queue.ByRunnerClass[publisherScaleSetName].P95Seconds != 300 {
		t.Fatalf("publisher queue=%+v", got.Queue.ByRunnerClass[publisherScaleSetName])
	}
	if !got.Coverage.ExecutionLedger || got.Coverage.WasteFindings || got.Coverage.ExecutionJobs != 3 || got.Coverage.QueueSamples != 2 {
		t.Fatalf("coverage=%+v", got.Coverage)
	}
	if got.Coverage.QueueCoveragePercent < 66 || got.Coverage.QueueCoveragePercent > 67 {
		t.Fatalf("queue coverage=%v", got.Coverage.QueueCoveragePercent)
	}
	if got.Coverage.ValidationIdentityJobs != 1 || got.Coverage.ValidationExcludedJobs != 1 || got.Coverage.ValidationPendingJobs != 1 {
		t.Fatalf("validation coverage=%+v", got.Coverage)
	}
	if got.Coverage.ValidationExclusions["unsupported-isolation"] != 1 {
		t.Fatalf("validation exclusions=%v", got.Coverage.ValidationExclusions)
	}
}

func TestSummarizeCIEfficiencyDistinguishesMissingQueueSamples(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	workers := []core.Worker{{
		ID: "job", Repository: "eutelic/runnerhub",
		StartedAt: now.Add(-time.Minute), CompletedAt: now.Add(-30 * time.Second),
	}}
	got := summarizeCIEfficiencyAt("eutelic/runnerhub", "30d", 30*24*time.Hour, now, workers, nil, nil, true)
	if got.Queue.Overall.Samples != 0 || got.Queue.Overall.P50Seconds != nil || got.Queue.Overall.P95Seconds != nil {
		t.Fatalf("missing queue samples must stay unavailable: %+v", got.Queue.Overall)
	}
	if got.Coverage.QueueSamples != 0 || got.Coverage.QueueCoveragePercent != 0 {
		t.Fatalf("queue coverage=%+v", got.Coverage)
	}
	if got.Coverage.AnalysisWindowComplete {
		t.Fatalf("unknown analyzer window must not claim complete coverage: %+v", got.Coverage)
	}
}

func TestParseCIEfficiencyWindow(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"": 30 * 24 * time.Hour,
		"7d": 7 * 24 * time.Hour,
		"30d": 30 * 24 * time.Hour,
		"90d": 90 * 24 * time.Hour,
	} {
		if _, got, err := parseCIEfficiencyWindow(raw); err != nil || got != want {
			t.Fatalf("window %q got=%v err=%v want=%v", raw, got, err, want)
		}
	}
	if _, _, err := parseCIEfficiencyWindow("14d"); err == nil {
		t.Fatal("expected unsupported window to fail")
	}
}

func TestCIEfficiencyEndpoint(t *testing.T) {
	ctxNow := time.Now().UTC()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertPool(context.Background(), core.RunnerPool{ID: "normal", ScaleSetName: "runnerhub", Labels: []string{"runnerhub"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorker(context.Background(), core.Worker{
		ID: "job", PoolID: "normal", Repository: "eutelic/runnerhub",
		QueuedAt: ctxNow.Add(-2 * time.Minute), StartedAt: ctxNow.Add(-time.Minute), CompletedAt: ctxNow.Add(-30 * time.Second),
		Resources: core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCIFinding(context.Background(), "eutelic/runnerhub", ciaudit.Finding{
		JobID:           "job",
		DetectorID:      "post-merge-validation",
		Class:           ciaudit.EvidenceConfirmed,
		AnalysisVersion: ciaudit.AnalyzerVersion,
		Interval: ciaudit.Interval{
			Start: ctxNow.Add(-45 * time.Second),
			End:   ctxNow.Add(-30 * time.Second),
		},
		Evidence: []string{"merged before validation completed"},
	}); err != nil {
		t.Fatal(err)
	}

	status := cireconcile.NewStatus(cireconcile.DefaultAnalyticsLookback)
	rec := &cireconcile.Reconciler{Status: status, Lookback: cireconcile.DefaultAnalyticsLookback, Now: func() time.Time { return ctxNow }}
	if err := rec.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st, CIReconcilerStatus: status}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/eutelic/runnerhub/ci-efficiency?window=7d", nil)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got ciEfficiencyResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Repository != "eutelic/runnerhub" || got.Window != "7d" || got.Jobs != 1 {
		t.Fatalf("response=%+v", got)
	}
	if !got.Coverage.WasteFindings || got.Waste.Findings != 1 || !got.Coverage.AnalysisWindowComplete {
		t.Fatalf("waste coverage=%+v waste=%+v", got.Coverage, got.Waste)
	}
	if got.Coverage.QueueSamples != 1 || got.Coverage.QueueCoveragePercent != 100 {
		t.Fatalf("queue coverage=%+v", got.Coverage)
	}
	if got.Analyzer.State != "healthy" || got.Analyzer.LookbackSeconds != int64(cireconcile.DefaultAnalyticsLookback/time.Second) {
		t.Fatalf("analyzer=%+v", got.Analyzer)
	}
	if got.Waste.Confirmed.RunnerSeconds != 15 || got.Waste.ConfirmedPercent != 50 {
		t.Fatalf("confirmed waste=%+v", got.Waste)
	}
	if len(got.Findings) != 1 || got.Findings[0].DetectorID != "post-merge-validation" {
		t.Fatalf("finding details=%+v", got.Findings)
	}
	if got.Findings[0].SuggestedFix == "" || len(got.Findings[0].Evidence) == 0 {
		t.Fatalf("finding must expose evidence and remediation: %+v", got.Findings[0])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/repositories/eutelic/runnerhub/ci-efficiency?window=14d", nil)
	rr = httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid window status=%d body=%s", rr.Code, rr.Body.String())
	}
}
