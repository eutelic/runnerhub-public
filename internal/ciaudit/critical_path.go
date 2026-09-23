package ciaudit

import (
	"fmt"
	"sort"
)

// CriticalJob is the normalized workflow dependency/duration input used for
// pipeline-latency analysis. DurationSeconds is execution time, not queue time.
type CriticalJob struct {
	ID              string
	Needs           []string
	DurationSeconds float64
}

type CriticalPathReport struct {
	TotalSeconds            float64  `json:"totalSeconds"`
	Path                    []string `json:"path,omitempty"`
	OptimizedSeconds        float64  `json:"optimizedSeconds,omitempty"`
	PotentialSavingsSeconds float64  `json:"potentialSavingsSeconds,omitempty"`
}

// AnalyzeCriticalPath computes the longest needs-path before and after removing
// jobs that a separate evidence layer has classified as safely avoidable.
// Queue latency intentionally remains outside this calculation.
func AnalyzeCriticalPath(jobs []CriticalJob, removable map[string]bool) (CriticalPathReport, error) {
	byID := map[string]CriticalJob{}
	for _, job := range jobs {
		if job.ID == "" || job.DurationSeconds < 0 {
			return CriticalPathReport{}, fmt.Errorf("invalid critical-path job")
		}
		if _, exists := byID[job.ID]; exists {
			return CriticalPathReport{}, fmt.Errorf("duplicate critical-path job %q", job.ID)
		}
		byID[job.ID] = job
	}
	total, path, err := longestPath(byID, nil)
	if err != nil {
		return CriticalPathReport{}, err
	}
	optimized, _, err := longestPath(byID, removable)
	if err != nil {
		return CriticalPathReport{}, err
	}
	savings := total - optimized
	if savings < 0 {
		savings = 0
	}
	return CriticalPathReport{TotalSeconds: total, Path: path, OptimizedSeconds: optimized, PotentialSavingsSeconds: savings}, nil
}

func longestPath(jobs map[string]CriticalJob, removed map[string]bool) (float64, []string, error) {
	memo := map[string]float64{}
	paths := map[string][]string{}
	visiting := map[string]bool{}
	var visit func(string) (float64, []string, error)
	visit = func(id string) (float64, []string, error) {
		if removed != nil && removed[id] {
			// A removed job contributes no duration; its dependencies remain as
			// prerequisites for downstream jobs.
			job, ok := jobs[id]
			if !ok {
				return 0, nil, fmt.Errorf("unknown job %q", id)
			}
			var best float64
			var bestPath []string
			for _, dep := range job.Needs {
				value, path, err := visit(dep)
				if err != nil {
					return 0, nil, err
				}
				if value > best {
					best, bestPath = value, path
				}
			}
			return best, append([]string(nil), bestPath...), nil
		}
		if value, ok := memo[id]; ok {
			return value, append([]string(nil), paths[id]...), nil
		}
		job, ok := jobs[id]
		if !ok {
			return 0, nil, fmt.Errorf("unknown job %q", id)
		}
		if visiting[id] {
			return 0, nil, fmt.Errorf("workflow needs cycle at %q", id)
		}
		visiting[id] = true
		defer delete(visiting, id)
		var best float64
		var bestPath []string
		deps := append([]string(nil), job.Needs...)
		sort.Strings(deps)
		for _, dep := range deps {
			value, path, err := visit(dep)
			if err != nil {
				return 0, nil, err
			}
			if value > best {
				best, bestPath = value, path
			}
		}
		value := best + job.DurationSeconds
		path := append(append([]string(nil), bestPath...), id)
		memo[id], paths[id] = value, path
		return value, append([]string(nil), path...), nil
	}
	var best float64
	var bestPath []string
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		value, path, err := visit(id)
		if err != nil {
			return 0, nil, err
		}
		if value > best {
			best, bestPath = value, path
		}
	}
	return best, bestPath, nil
}
