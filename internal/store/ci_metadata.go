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
	CreatedAt          time.Time `json:"createdAt,omitempty"`
	RunStartedAt       time.Time `json:"runStartedAt,omitempty"`
	UpdatedAt          time.Time `json:"updatedAt,omitempty"`
	PullRequestNumbers []int     `json:"pullRequestNumbers,omitempty"`
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
	UpsertCIPullRequest(context.Context, CIPullRequest) error
	GetCIPullRequest(context.Context, string, int) (CIPullRequest, error)
}
