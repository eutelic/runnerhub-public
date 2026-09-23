package githubrunner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestHTTPBackendCIMetadataEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/eutelic/runnerhub/actions/runs/77":
			fmt.Fprint(w, `{"id":77,"run_attempt":2,"name":"ci","path":".github/workflows/ci.yml","event":"pull_request","status":"completed","conclusion":"success","head_branch":"feature","head_sha":"abc","head_commit":{"tree_id":"tree-abc"},"created_at":"2026-09-22T10:00:00Z","run_started_at":"2026-09-22T10:01:00Z","updated_at":"2026-09-22T10:10:00Z","pull_requests":[{"number":12}]}`)
		case "/repos/eutelic/runnerhub/actions/runs/77/attempts/1":
			fmt.Fprint(w, `{"id":77,"run_attempt":1,"name":"ci","path":".github/workflows/ci.yml","event":"pull_request","status":"completed","conclusion":"failure","head_branch":"feature","head_sha":"abc","head_commit":{"tree_id":"tree-abc"},"created_at":"2026-09-22T10:00:00Z","run_started_at":"2026-09-22T10:01:00Z","updated_at":"2026-09-22T10:05:00Z","pull_requests":[{"number":12}]}`)
		case "/repos/eutelic/runnerhub/actions/runs/77/attempts/1/jobs":
			fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":991,"name":"core","status":"completed","conclusion":"failure","runner_name":"runner-1","runner_group_name":"Default","labels":["runnerhub"],"started_at":"2026-09-22T10:01:00Z","completed_at":"2026-09-22T10:04:00Z","steps":[]}]}`)
		case "/repos/eutelic/runnerhub/pulls/12":
			fmt.Fprint(w, `{"number":12,"state":"closed","merged_at":"2026-09-22T10:03:00Z","head":{"ref":"feature","sha":"abc"},"base":{"ref":"main"}}`)
		case "/repos/eutelic/runnerhub/contents/.github/workflows/ci.yml":
			if r.URL.Query().Get("ref") != "abc" {
				t.Fatalf("ref=%q", r.URL.Query().Get("ref"))
			}
			fmt.Fprint(w, `{"type":"file","sha":"workflow-blob","encoding":"base64","content":"bmFtZTogY2kK"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	backend, err := NewHTTPBackendWithBaseURL(server.Client(), staticTokenSource("token"), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	scope := core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"}

	run, err := backend.GetWorkflowRun(context.Background(), scope, 77)
	if err != nil {
		t.Fatal(err)
	}
	if run.Attempt != 2 || run.Name != "ci" || run.HeadTreeSHA != "tree-abc" || len(run.PullRequestNumbers) != 1 || run.PullRequestNumbers[0] != 12 {
		t.Fatalf("run=%+v", run)
	}

	attempt, err := backend.GetWorkflowRunAttempt(context.Background(), scope, 77, 1)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Attempt != 1 || attempt.Conclusion != "failure" {
		t.Fatalf("attempt=%+v", attempt)
	}

	jobs, err := backend.ListWorkflowJobsAttempt(context.Background(), scope, 77, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != 991 || jobs[0].RunnerName != "runner-1" {
		t.Fatalf("jobs=%+v", jobs)
	}

	file, err := backend.GetRepositoryFile(context.Background(), scope, ".github/workflows/ci.yml", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if file.SHA != "workflow-blob" || string(file.Content) != "name: ci\n" {
		t.Fatalf("file=%+v content=%q", file, string(file.Content))
	}

	pr, err := backend.GetPullRequest(context.Background(), scope, 12)
	if err != nil {
		t.Fatal(err)
	}
	wantMerged := time.Date(2026, 9, 22, 10, 3, 0, 0, time.UTC)
	if !pr.MergedAt.Equal(wantMerged) || pr.HeadRef != "feature" || pr.BaseRef != "main" {
		t.Fatalf("pr=%+v", pr)
	}
}
