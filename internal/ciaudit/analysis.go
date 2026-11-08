package ciaudit

import (
	"sort"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

// EvidenceClass describes how strongly RunnerHub can justify a CI inefficiency.
// Only confirmed and input/dependency-proven findings contribute to provable
// waste totals. Advisory findings remain visible without inflating the headline.
type EvidenceClass string

const (
	EvidenceConfirmed        EvidenceClass = "confirmed"
	EvidenceInputEquivalent  EvidenceClass = "input-equivalent"
	EvidenceDependencyProven EvidenceClass = "dependency-proven"
	EvidenceHighConfidence   EvidenceClass = "high-confidence"
	EvidenceHeuristic        EvidenceClass = "heuristic"
	EvidenceUnknown          EvidenceClass = "unknown"
)

type Interval struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type Finding struct {
	JobID           string        `json:"jobId"`
	DetectorID      string        `json:"detectorId"`
	Class           EvidenceClass `json:"class"`
	AnalysisVersion string        `json:"analysisVersion"`
	Interval        Interval      `json:"interval"`
	Evidence        []string      `json:"evidence,omitempty"`
}

type JobExecution struct {
	ID            string                    `json:"id"`
	Repository    string                    `json:"repository,omitempty"`
	WorkflowRef   string                    `json:"workflowRef,omitempty"`
	WorkflowRunID int64                     `json:"workflowRunId,omitempty"`
	RunAttempt    int                       `json:"runAttempt,omitempty"`
	GitHubJobID   string                    `json:"githubJobId,omitempty"`
	StartedAt     time.Time                 `json:"startedAt"`
	CompletedAt   time.Time                 `json:"completedAt"`
	Resources     core.Resources            `json:"resources"`
	Usage         core.ResourceUsageSummary `json:"usage,omitempty"`
}

type ResourceTotals struct {
	RunnerSeconds          float64 `json:"runnerSeconds"`
	AllocatedCPUSeconds    float64 `json:"allocatedCpuSeconds"`
	AllocatedRAMGiBSeconds float64 `json:"allocatedRamGiBSeconds"`
	EstimatedCPUSeconds    float64 `json:"estimatedCpuSeconds,omitempty"`
	EstimatedRAMGiBSeconds float64 `json:"estimatedRamGiBSeconds,omitempty"`
}

type Report struct {
	Total                ResourceTotals `json:"total"`
	ConfirmedWaste       ResourceTotals `json:"confirmedWaste"`
	ProvablyAvoidable    ResourceTotals `json:"provablyAvoidable"`
	PotentialSavings     ResourceTotals `json:"potentialSavings"`
	ConfirmedWastePct    float64        `json:"confirmedWastePercent"`
	ProvablyAvoidablePct float64        `json:"provablyAvoidablePercent"`
	PotentialSavingsPct  float64        `json:"potentialSavingsPercent"`
}

func isProvable(class EvidenceClass) bool {
	return class == EvidenceConfirmed || class == EvidenceInputEquivalent || class == EvidenceDependencyProven
}

// ExecutionFromWorker projects RunnerHub's existing worker history into the
// analysis model. The worker ID remains the execution identity; GitHub run/job
// identifiers are evidence used by detectors, not a substitute for a unique
// RunnerHub execution record.
func ExecutionFromWorker(worker core.Worker) (JobExecution, bool) {
	job := JobExecution{
		ID:            worker.ID,
		Repository:    worker.Repository,
		WorkflowRef:   worker.WorkflowRef,
		WorkflowRunID: worker.WorkflowRunID,
		RunAttempt:    worker.WorkflowRunAttempt,
		GitHubJobID:   worker.JobID,
		StartedAt:     worker.StartedAt,
		CompletedAt:   worker.CompletedAt,
		Resources:     worker.Resources,
		Usage:         worker.Usage,
	}
	return job, validJob(job)
}

// Summarize computes resource-weighted CI waste without double-counting.
// Findings are clipped to the corresponding job runtime and overlapping
// intervals are unioned independently for confirmed and all provable waste.
func Summarize(jobs []JobExecution, findings []Finding) Report {
	byJob := make(map[string][]Finding, len(findings))
	for _, finding := range findings {
		byJob[finding.JobID] = append(byJob[finding.JobID], finding)
	}

	var report Report
	// Replayed/paginated worker history can contain the same execution more than
	// once. Count each stable worker ID only once to avoid inflating both the
	// measured denominator and the reported waste.
	// Prefer the most complete observation of a replayed execution. An older
	// page may contain an earlier completion timestamp; choosing the first
	// occurrence would make totals depend on pagination order.
	selected := make(map[string]JobExecution, len(jobs))
	for _, job := range jobs {
		if !validJob(job) {
			continue
		}
		previous, exists := selected[job.ID]
		if !exists || job.StartedAt.After(previous.StartedAt) ||
			(job.StartedAt.Equal(previous.StartedAt) && (job.CompletedAt.After(previous.CompletedAt) ||
				(job.CompletedAt.Equal(previous.CompletedAt) && job.Usage.Samples > previous.Usage.Samples))) {
			selected[job.ID] = job
		}
	}
	for _, job := range selected {
		full := []Interval{{Start: job.StartedAt, End: job.CompletedAt}}
		report.Total = addTotals(report.Total, measure(job, full))

		var confirmed []Interval
		var provable []Interval
		var potential []Interval
		for _, finding := range byJob[job.ID] {
			clipped, ok := clip(finding.Interval, full[0])
			if !ok {
				continue
			}
			if finding.Class == EvidenceConfirmed {
				confirmed = append(confirmed, clipped)
			}
			if isProvable(finding.Class) {
				provable = append(provable, clipped)
			}
			if finding.Class == EvidenceHighConfidence {
				potential = append(potential, clipped)
			}
		}
		report.ConfirmedWaste = addTotals(report.ConfirmedWaste, measure(job, union(confirmed)))
		report.ProvablyAvoidable = addTotals(report.ProvablyAvoidable, measure(job, union(provable)))
		report.PotentialSavings = addTotals(report.PotentialSavings, measure(job, union(potential)))
	}

	if report.Total.RunnerSeconds > 0 {
		report.ConfirmedWastePct = report.ConfirmedWaste.RunnerSeconds / report.Total.RunnerSeconds * 100
		report.ProvablyAvoidablePct = report.ProvablyAvoidable.RunnerSeconds / report.Total.RunnerSeconds * 100
		report.PotentialSavingsPct = report.PotentialSavings.RunnerSeconds / report.Total.RunnerSeconds * 100
	}
	return report
}

// PostMergeFinding returns confirmed waste for side-effect-free validation that
// kept consuming a runner after its pull request had already merged.
func PostMergeFinding(job JobExecution, mergedAt time.Time, sideEffectFree bool) (Finding, bool) {
	if !sideEffectFree || !validJob(job) || mergedAt.IsZero() || !mergedAt.Before(job.CompletedAt) {
		return Finding{}, false
	}
	start := mergedAt
	if job.StartedAt.After(start) {
		start = job.StartedAt
	}
	return Finding{
		JobID:           job.ID,
		DetectorID:      "post-merge-validation",
		Class:           EvidenceConfirmed,
		AnalysisVersion: AnalyzerVersion,
		Interval:        Interval{Start: start, End: job.CompletedAt},
	}, true
}

// SupersededFinding returns confirmed waste after an ordinary validation task
// became obsolete. Callers must only set safeToSupersede when workflow
// semantics establish that the older execution has no independent release or
// side-effect value.
func SupersededFinding(job JobExecution, supersededAt time.Time, safeToSupersede bool) (Finding, bool) {
	if !safeToSupersede || !validJob(job) || supersededAt.IsZero() || !supersededAt.Before(job.CompletedAt) {
		return Finding{}, false
	}
	start := supersededAt
	if job.StartedAt.After(start) {
		start = job.StartedAt
	}
	return Finding{
		JobID:           job.ID,
		DetectorID:      "superseded-validation",
		Class:           EvidenceConfirmed,
		AnalysisVersion: AnalyzerVersion,
		Interval:        Interval{Start: start, End: job.CompletedAt},
	}, true
}

// StaleSnapshotPublicationFinding reports compute spent after a newer main
// snapshot superseded a mutable branch publication. Immutable tag/release
// publication must never call this detector.
func StaleSnapshotPublicationFinding(job JobExecution, supersededAt time.Time, mutableSnapshot bool) (Finding, bool) {
	if !mutableSnapshot || !validJob(job) || supersededAt.IsZero() || !supersededAt.Before(job.CompletedAt) {
		return Finding{}, false
	}
	start := supersededAt
	if job.StartedAt.After(start) {
		start = job.StartedAt
	}
	return Finding{
		JobID:           job.ID,
		DetectorID:      "stale-snapshot-publication",
		Class:           EvidenceConfirmed,
		AnalysisVersion: AnalyzerVersion,
		Interval:        Interval{Start: start, End: job.CompletedAt},
	}, true
}

func validJob(job JobExecution) bool {
	return job.ID != "" && !job.StartedAt.IsZero() && !job.CompletedAt.IsZero() && job.StartedAt.Before(job.CompletedAt)
}

func measure(job JobExecution, intervals []Interval) ResourceTotals {
	var seconds float64
	for _, interval := range intervals {
		if interval.Start.Before(interval.End) {
			seconds += interval.End.Sub(interval.Start).Seconds()
		}
	}
	if seconds <= 0 {
		return ResourceTotals{}
	}
	out := ResourceTotals{
		RunnerSeconds:          seconds,
		AllocatedCPUSeconds:    seconds * float64(job.Resources.CPUMillis) / 1000,
		AllocatedRAMGiBSeconds: seconds * float64(job.Resources.MemoryBytes) / float64(uint64(1)<<30),
	}
	if job.Usage.Samples > 0 {
		out.EstimatedCPUSeconds = seconds * float64(job.Usage.AverageCPUMillis) / 1000
		out.EstimatedRAMGiBSeconds = seconds * float64(job.Usage.AverageMemoryBytes) / float64(uint64(1)<<30)
	}
	return out
}

func addTotals(a, b ResourceTotals) ResourceTotals {
	return ResourceTotals{
		RunnerSeconds:          a.RunnerSeconds + b.RunnerSeconds,
		AllocatedCPUSeconds:    a.AllocatedCPUSeconds + b.AllocatedCPUSeconds,
		AllocatedRAMGiBSeconds: a.AllocatedRAMGiBSeconds + b.AllocatedRAMGiBSeconds,
		EstimatedCPUSeconds:    a.EstimatedCPUSeconds + b.EstimatedCPUSeconds,
		EstimatedRAMGiBSeconds: a.EstimatedRAMGiBSeconds + b.EstimatedRAMGiBSeconds,
	}
}

func clip(candidate, bounds Interval) (Interval, bool) {
	start := candidate.Start
	if start.Before(bounds.Start) {
		start = bounds.Start
	}
	end := candidate.End
	if end.After(bounds.End) {
		end = bounds.End
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return Interval{}, false
	}
	return Interval{Start: start, End: end}, true
}

func union(intervals []Interval) []Interval {
	filtered := intervals[:0]
	for _, interval := range intervals {
		if !interval.Start.IsZero() && !interval.End.IsZero() && interval.Start.Before(interval.End) {
			filtered = append(filtered, interval)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Start.Equal(filtered[j].Start) {
			return filtered[i].End.Before(filtered[j].End)
		}
		return filtered[i].Start.Before(filtered[j].Start)
	})

	out := []Interval{filtered[0]}
	for _, interval := range filtered[1:] {
		last := &out[len(out)-1]
		if !interval.Start.After(last.End) {
			if interval.End.After(last.End) {
				last.End = interval.End
			}
			continue
		}
		out = append(out, interval)
	}
	return out
}
