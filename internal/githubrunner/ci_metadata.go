package githubrunner

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

type WorkflowRunMetadata struct {
	ID                 int64     `json:"id"`
	Attempt            int       `json:"attempt"`
	Name               string    `json:"name"`
	Path               string    `json:"path,omitempty"`
	Event              string    `json:"event,omitempty"`
	Status             string    `json:"status,omitempty"`
	Conclusion         string    `json:"conclusion,omitempty"`
	HeadBranch         string    `json:"headBranch,omitempty"`
	HeadSHA            string    `json:"headSha,omitempty"`
	HeadTreeSHA        string    `json:"headTreeSha,omitempty"`
	CreatedAt          time.Time `json:"createdAt,omitempty"`
	RunStartedAt       time.Time `json:"runStartedAt,omitempty"`
	UpdatedAt          time.Time `json:"updatedAt,omitempty"`
	PullRequestNumbers []int     `json:"pullRequestNumbers,omitempty"`
}

type RepositoryFile struct {
	SHA     string `json:"sha"`
	Content []byte `json:"-"`
}

type PullRequestMetadata struct {
	Number   int       `json:"number"`
	State    string    `json:"state,omitempty"`
	MergedAt time.Time `json:"mergedAt,omitempty"`
	HeadRef  string    `json:"headRef,omitempty"`
	HeadSHA  string    `json:"headSha,omitempty"`
	BaseRef  string    `json:"baseRef,omitempty"`
}

type CIMetadataBackend interface {
	GetWorkflowRun(context.Context, core.GitHubScope, int64) (WorkflowRunMetadata, error)
	GetWorkflowRunAttempt(context.Context, core.GitHubScope, int64, int) (WorkflowRunMetadata, error)
	ListWorkflowJobsAttempt(context.Context, core.GitHubScope, int64, int) ([]WorkflowJob, error)
	GetPullRequest(context.Context, core.GitHubScope, int) (PullRequestMetadata, error)
	GetRepositoryFile(context.Context, core.GitHubScope, string, string) (RepositoryFile, error)
}

type workflowRunPayload struct {
	ID           int64     `json:"id"`
	RunAttempt   int       `json:"run_attempt"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Event        string    `json:"event"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	HeadBranch   string    `json:"head_branch"`
	HeadSHA      string    `json:"head_sha"`
	HeadCommit struct {
		TreeID string `json:"tree_id"`
	} `json:"head_commit"`
	CreatedAt    time.Time `json:"created_at"`
	RunStartedAt time.Time `json:"run_started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	PullRequests []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
}

func workflowRunMetadata(payload workflowRunPayload) WorkflowRunMetadata {
	prs := make([]int, 0, len(payload.PullRequests))
	for _, pr := range payload.PullRequests {
		if pr.Number > 0 {
			prs = append(prs, pr.Number)
		}
	}
	return WorkflowRunMetadata{
		ID: payload.ID, Attempt: payload.RunAttempt, Name: payload.Name, Path: payload.Path,
		Event: payload.Event, Status: payload.Status, Conclusion: payload.Conclusion,
		HeadBranch: payload.HeadBranch, HeadSHA: payload.HeadSHA, HeadTreeSHA: payload.HeadCommit.TreeID, CreatedAt: payload.CreatedAt,
		RunStartedAt: payload.RunStartedAt, UpdatedAt: payload.UpdatedAt, PullRequestNumbers: prs,
	}
}

func validateRepositoryScope(scope core.GitHubScope) error {
	if err := core.ValidateGitHubScope(scope); err != nil {
		return err
	}
	if scope.Kind != core.ScopeRepository {
		return errors.New("CI metadata requires a repository scope")
	}
	return nil
}

func (b *HTTPBackend) GetWorkflowRun(ctx context.Context, scope core.GitHubScope, runID int64) (WorkflowRunMetadata, error) {
	if err := validateRepositoryScope(scope); err != nil {
		return WorkflowRunMetadata{}, err
	}
	if runID <= 0 {
		return WorkflowRunMetadata{}, errors.New("workflow run id must be positive")
	}
	var payload workflowRunPayload
	path := b.scopeBasePath(scope) + "/actions/runs/" + strconv.FormatInt(runID, 10)
	if err := b.doJSON(ctx, scope, http.MethodGet, path, nil, http.StatusOK, &payload); err != nil {
		return WorkflowRunMetadata{}, err
	}
	return workflowRunMetadata(payload), nil
}

func (b *HTTPBackend) GetWorkflowRunAttempt(ctx context.Context, scope core.GitHubScope, runID int64, attempt int) (WorkflowRunMetadata, error) {
	if err := validateRepositoryScope(scope); err != nil {
		return WorkflowRunMetadata{}, err
	}
	if runID <= 0 || attempt <= 0 {
		return WorkflowRunMetadata{}, errors.New("workflow run id and attempt must be positive")
	}
	var payload workflowRunPayload
	path := b.scopeBasePath(scope) + "/actions/runs/" + strconv.FormatInt(runID, 10) + "/attempts/" + strconv.Itoa(attempt)
	if err := b.doJSON(ctx, scope, http.MethodGet, path, nil, http.StatusOK, &payload); err != nil {
		return WorkflowRunMetadata{}, err
	}
	return workflowRunMetadata(payload), nil
}

func (b *HTTPBackend) ListWorkflowJobsAttempt(ctx context.Context, scope core.GitHubScope, runID int64, attempt int) ([]WorkflowJob, error) {
	if err := validateRepositoryScope(scope); err != nil {
		return nil, err
	}
	if runID <= 0 || attempt <= 0 {
		return nil, errors.New("workflow run id and attempt must be positive")
	}
	out := []WorkflowJob{}
	for page := 1; ; page++ {
		var response struct {
			TotalCount int `json:"total_count"`
			Jobs []struct {
				ID int64 `json:"id"`
				Name string `json:"name"`
				Status string `json:"status"`
				Conclusion string `json:"conclusion"`
				RunnerName string `json:"runner_name"`
				RunnerGroupName string `json:"runner_group_name"`
				Labels []string `json:"labels"`
				StartedAt time.Time `json:"started_at"`
				CompletedAt time.Time `json:"completed_at"`
				Steps []struct {
					Name string `json:"name"`
					Status string `json:"status"`
					Conclusion string `json:"conclusion"`
					Number int `json:"number"`
				} `json:"steps"`
			} `json:"jobs"`
		}
		path := b.scopeBasePath(scope) + "/actions/runs/" + strconv.FormatInt(runID, 10) + "/attempts/" + strconv.Itoa(attempt) + "/jobs?per_page=100&page=" + strconv.Itoa(page)
		if err := b.doJSON(ctx, scope, http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
			return nil, err
		}
		for _, job := range response.Jobs {
			steps := make([]JobStep, 0, len(job.Steps))
			for _, step := range job.Steps {
				steps = append(steps, JobStep{Name: step.Name, Status: step.Status, Conclusion: step.Conclusion, Number: step.Number})
			}
			out = append(out, WorkflowJob{
				ID: job.ID, Name: job.Name, Status: job.Status, Conclusion: job.Conclusion,
				RunnerName: job.RunnerName, RunnerGroupName: job.RunnerGroupName,
				Labels: append([]string(nil), job.Labels...), StartedAt: job.StartedAt,
				CompletedAt: job.CompletedAt, Steps: steps,
			})
		}
		if len(response.Jobs) < 100 || len(out) >= response.TotalCount {
			break
		}
	}
	return out, nil
}

func (b *HTTPBackend) GetPullRequest(ctx context.Context, scope core.GitHubScope, number int) (PullRequestMetadata, error) {
	if err := validateRepositoryScope(scope); err != nil {
		return PullRequestMetadata{}, err
	}
	if number <= 0 {
		return PullRequestMetadata{}, errors.New("pull request number must be positive")
	}
	var payload struct {
		Number int `json:"number"`
		State string `json:"state"`
		MergedAt *time.Time `json:"merged_at"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	path := b.scopeBasePath(scope) + "/pulls/" + strconv.Itoa(number)
	if err := b.doJSON(ctx, scope, http.MethodGet, path, nil, http.StatusOK, &payload); err != nil {
		return PullRequestMetadata{}, err
	}
	out := PullRequestMetadata{Number: payload.Number, State: payload.State, HeadRef: payload.Head.Ref, HeadSHA: payload.Head.SHA, BaseRef: payload.Base.Ref}
	if payload.MergedAt != nil {
		out.MergedAt = payload.MergedAt.UTC()
	}
	return out, nil
}


func (b *HTTPBackend) GetRepositoryFile(ctx context.Context, scope core.GitHubScope, path, ref string) (RepositoryFile, error) {
	if err := validateRepositoryScope(scope); err != nil {
		return RepositoryFile{}, err
	}
	path = strings.Trim(strings.TrimSpace(path), "/")
	ref = strings.TrimSpace(ref)
	if path == "" || ref == "" {
		return RepositoryFile{}, errors.New("repository file requires path and ref")
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	var payload struct {
		Type     string `json:"type"`
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	endpoint := b.scopeBasePath(scope) + "/contents/" + strings.Join(parts, "/") + "?ref=" + url.QueryEscape(ref)
	if err := b.doJSON(ctx, scope, http.MethodGet, endpoint, nil, http.StatusOK, &payload); err != nil {
		return RepositoryFile{}, err
	}
	if payload.Type != "file" || strings.TrimSpace(payload.SHA) == "" {
		return RepositoryFile{}, errors.New("github returned incomplete repository file metadata")
	}
	if payload.Encoding != "base64" || strings.TrimSpace(payload.Content) == "" {
		return RepositoryFile{}, errors.New("github repository file content is not inline base64")
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return RepositoryFile{}, fmt.Errorf("decode github repository file: %w", err)
	}
	return RepositoryFile{SHA: payload.SHA, Content: content}, nil
}
