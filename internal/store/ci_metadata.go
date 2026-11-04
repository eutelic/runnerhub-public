package store

import (
	"context"
	"time"
)

type CIWorkflowRun struct {
	Repository         string    `json:"repository"`
	RunID              int64     `json:"runId"`
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

type CIWorkflowRunQuarantine struct {
	Repository    string    `json:"repository"`
	RunID         int64     `json:"runId"`
	FailureCount  int       `json:"failureCount"`
	Reason        string    `json:"reason"`
	LastFailureAt time.Time `json:"lastFailureAt"`
	RetryAfter    time.Time `json:"retryAfter"`
}

type CIPullRequest struct {
	Repository string    `json:"repository"`
	Number     int       `json:"number"`
	State      string    `json:"state,omitempty"`
	MergedAt   time.Time `json:"mergedAt,omitempty"`
	HeadRef    string    `json:"headRef,omitempty"`
	HeadSHA    string    `json:"headSha,omitempty"`
	BaseRef    string    `json:"baseRef,omitempty"`
}

type CIMetadataStore interface {
	UpsertCIWorkflowRun(context.Context, CIWorkflowRun) error
	ListCIWorkflowRuns(context.Context, string, time.Time, time.Time) ([]CIWorkflowRun, error)
	GetCIWorkflowRunQuarantine(context.Context, string, int64) (CIWorkflowRunQuarantine, error)
	UpsertCIWorkflowRunQuarantine(context.Context, CIWorkflowRunQuarantine) error
	DeleteCIWorkflowRunQuarantine(context.Context, string, int64) error
	UpsertCIPullRequest(context.Context, CIPullRequest) error
	GetCIPullRequest(context.Context, string, int) (CIPullRequest, error)
}
