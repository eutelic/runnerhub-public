package api

import (
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
)

func TestMatchWorkflowJobPrefersRunnerName(t *testing.T) {
	wk := core.Worker{RunnerName:"runnerhub-abc", JobName:"test"}
	jobs := []githubrunner.WorkflowJob{
		{ID:1,Name:"test",RunnerName:"runnerhub-other"},
		{ID:2,Name:"different display name",RunnerName:"runnerhub-abc"},
	}
	got, ok := matchWorkflowJob(wk,jobs)
	if !ok || got.ID != 2 { t.Fatalf("unexpected match: ok=%v job=%+v",ok,got) }
}

func TestSummarizeWorkflowJobStepProgress(t *testing.T) {
	wk := core.Worker{ID:"worker-1",WorkflowRunID:77}
	job := githubrunner.WorkflowJob{
		ID:123,Status:"in_progress",
		Steps:[]githubrunner.JobStep{
			{Name:"Set up job",Status:"completed",Conclusion:"success"},
			{Name:"Run tests",Status:"in_progress"},
			{Name:"Complete job",Status:"queued"},
		},
	}
	got := summarizeWorkflowJob(wk,job)
	if !got.Available || got.CompletedSteps != 1 || got.TotalSteps != 3 || got.Percent != 33 || got.CurrentStep != "Run tests" {
		t.Fatalf("unexpected progress: %+v",got)
	}
}
