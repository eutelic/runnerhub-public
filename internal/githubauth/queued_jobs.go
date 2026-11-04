package githubauth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// QueuedJob is a bounded, read-only snapshot of a GitHub Actions job.
// Only job IDs, names, states and requested labels are returned; workflow
// logs, secrets and environment variables are not fetched.
type QueuedJob struct {
	ID         int64    `json:"id"`
	RunID      int64    `json:"runId"`
	RunURL     string   `json:"runUrl"`
	URL        string   `json:"url"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Labels     []string `json:"labels"`
	RunnerName string   `json:"runnerName,omitempty"`
}

type QueuedJobsReport struct {
	Repository  string      `json:"repository"`
	Jobs        []QueuedJob `json:"jobs"`
	CheckedRuns int         `json:"checkedRuns"`
	Truncated   bool        `json:"truncated"`
}

// QueuedJobs is called on explicit diagnostics refresh only, never on the
// dashboard's periodic polling loop. Limit GitHub calls to at most 10 runs and
// 100 jobs per run to avoid amplifying existing rate-limit incidents.
func (m *Manager) QueuedJobs(r *http.Request, fullName string) (QueuedJobsReport, error) {
	s, ok := m.sessionFor(r, false)
	if !ok {
		return QueuedJobsReport{}, errors.New("github login required")
	}
	repos, err := m.cachedRepositories(r, s)
	if err != nil {
		return QueuedJobsReport{}, err
	}
	var matched *ConnectedRepository
	for i := range repos {
		if strings.EqualFold(repos[i].FullName, strings.TrimSpace(fullName)) {
			matched = &repos[i]
			break
		}
	}
	if matched == nil {
		return QueuedJobsReport{}, errors.New("repository is not exposed by the selected GitHub App installation")
	}
	if !matched.Private {
		return QueuedJobsReport{}, errors.New("public repositories require separate untrusted runner isolation")
	}
	parts := strings.Split(matched.FullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return QueuedJobsReport{}, errors.New("invalid repository identifier")
	}
	base := "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/actions"
	report := QueuedJobsReport{Repository: matched.FullName, Jobs: []QueuedJob{}}
	seen := map[int64]bool{}
	for _, runStatus := range []string{"queued", "in_progress"} {
		var runs struct {
			TotalCount   int `json:"total_count"`
			WorkflowRuns []struct {
				ID      int64  `json:"id"`
				HTMLURL string `json:"html_url"`
			} `json:"workflow_runs"`
		}
		if err := m.apiGET(r, s.Token, base+"/runs?status="+runStatus+"&per_page=5", &runs); err != nil {
			return QueuedJobsReport{}, fmt.Errorf("read %s GitHub Actions runs: %w", runStatus, err)
		}
		if runs.TotalCount > len(runs.WorkflowRuns) {
			report.Truncated = true
		}
		for _, run := range runs.WorkflowRuns {
			if run.ID <= 0 || seen[run.ID] {
				continue
			}
			seen[run.ID] = true
			report.CheckedRuns++
			var jobs struct {
				TotalCount int `json:"total_count"`
				Jobs []struct {
					ID         int64    `json:"id"`
					Name       string   `json:"name"`
					HTMLURL    string   `json:"html_url"`
					Status     string   `json:"status"`
					Labels     []string `json:"labels"`
					RunnerName string   `json:"runner_name"`
				} `json:"jobs"`
			}
			if err := m.apiGET(r, s.Token, fmt.Sprintf("%s/runs/%d/jobs?per_page=100", base, run.ID), &jobs); err != nil {
				return QueuedJobsReport{}, fmt.Errorf("read GitHub Actions jobs for run %d: %w", run.ID, err)
			}
			if jobs.TotalCount > len(jobs.Jobs) {
				report.Truncated = true
			}
			for _, job := range jobs.Jobs {
				if job.Status != "queued" && job.Status != "waiting" && job.Status != "pending" {
					continue
				}
				report.Jobs = append(report.Jobs, QueuedJob{
					ID: job.ID, RunID: run.ID, RunURL: run.HTMLURL,
					URL: job.HTMLURL, Name: job.Name, Status: job.Status,
					Labels: job.Labels, RunnerName: job.RunnerName,
				})
			}
		}
	}
	return report, nil
}
