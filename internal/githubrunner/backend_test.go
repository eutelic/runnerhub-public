package githubrunner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

type staticTokenSource string

func (s staticTokenSource) Token(context.Context, core.GitHubScope) (string, error) {
	return string(s), nil
}

func TestValidateJITConfigRequest(t *testing.T) {
	req := JITConfigRequest{
		Scope:         core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"},
		RunnerName:    "runnerhub-linux-small-a1b2c3",
		RunnerGroupID: 1,
		Labels:        []string{"linux-small"},
	}
	if err := ValidateJITConfigRequest(req); err != nil {
		t.Fatalf("expected valid request, got %v", err)
	}
}

func TestValidateJITConfigRequestRejectsMissingGroup(t *testing.T) {
	req := JITConfigRequest{
		Scope:      core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"},
		RunnerName: "runnerhub-linux-small-a1b2c3",
		Labels:     []string{"linux-small"},
	}
	if err := ValidateJITConfigRequest(req); err == nil {
		t.Fatal("expected missing runner group id to fail")
	}
}

func TestHTTPBackendGenerateRepositoryJITConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/eutelic/runnerhub/actions/runners/generate-jitconfig" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatal("missing bearer token")
		}
		if r.Header.Get("X-GitHub-Api-Version") != defaultAPIVersion {
			t.Fatal("missing github api version")
		}
		var body struct {
			Name          string   `json:"name"`
			RunnerGroupID int64    `json:"runner_group_id"`
			Labels        []string `json:"labels"`
			WorkFolder    string   `json:"work_folder"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Name != "runnerhub-a1" || body.RunnerGroupID != 1 || body.WorkFolder != "_work" {
			t.Fatalf("unexpected request body: %+v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"runner":{"id":23,"name":"runnerhub-a1"},"encoded_jit_config":"encoded-config"}`))
	}))
	defer server.Close()

	backend, err := NewHTTPBackendWithBaseURL(server.Client(), staticTokenSource("secret-token"), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := backend.GenerateJITConfig(context.Background(), JITConfigRequest{
		Scope:         core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"},
		RunnerName:    "runnerhub-a1",
		RunnerGroupID: 1,
		Labels:        []string{"linux-small"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.RunnerID != 23 || got.RunnerName != "runnerhub-a1" || got.EncodedConfig != "encoded-config" {
		t.Fatalf("unexpected jit config: %+v", got)
	}
}

func TestHTTPBackendGetOrganizationRunner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/orgs/example/actions/runners/42" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":42,"name":"build-42","status":"online","busy":true,"labels":[{"name":"self-hosted"},{"name":"gpu"}]}`))
	}))
	defer server.Close()

	backend, _ := NewHTTPBackendWithBaseURL(server.Client(), staticTokenSource("secret-token"), server.URL)
	got, err := backend.GetRunner(context.Background(), core.GitHubScope{Kind: core.ScopeOrganization, Owner: "example"}, 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || got.Status != "online" || !got.Busy || len(got.Labels) != 2 {
		t.Fatalf("unexpected runner: %+v", got)
	}
}

func TestHTTPBackendDeleteRepositoryRunner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/repos/eutelic/runnerhub/actions/runners/42" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	backend, _ := NewHTTPBackendWithBaseURL(server.Client(), staticTokenSource("secret-token"), server.URL)
	if err := backend.DeleteRunner(context.Background(), core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"}, 42); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPBackendDoesNotEchoTokenOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "permission denied", http.StatusForbidden)
	}))
	defer server.Close()

	const token = "should-never-appear"
	backend, _ := NewHTTPBackendWithBaseURL(server.Client(), staticTokenSource(token), server.URL)
	_, err := backend.GetRunner(context.Background(), core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"}, 42)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("error leaked github token")
	}
}

func TestHTTPBackendListWorkflowJobs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/eutelic/runnerhub/actions/runs/987/jobs" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("filter") != "latest" || r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"total_count":1,"jobs":[{"id":1234,"name":"test","status":"in_progress","conclusion":null,"runner_name":"runnerhub-abc","runner_group_name":"Default","labels":["runnerhub","linux"],"started_at":"2026-09-22T16:00:00Z","completed_at":null,"steps":[{"name":"Set up job","status":"completed","conclusion":"success","number":1},{"name":"Run tests","status":"in_progress","conclusion":null,"number":2},{"name":"Complete job","status":"queued","conclusion":null,"number":3}]}]}`))
	}))
	defer server.Close()

	backend, _ := NewHTTPBackendWithBaseURL(server.Client(), staticTokenSource("secret-token"), server.URL)
	jobs, err := backend.ListWorkflowJobs(context.Background(), core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"}, 987)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %d", len(jobs))
	}
	job := jobs[0]
	if job.ID != 1234 || job.RunnerName != "runnerhub-abc" || job.RunnerGroupName != "Default" || len(job.Labels) != 2 || len(job.Steps) != 3 {
		t.Fatalf("unexpected job: %+v", job)
	}
	wantStarted := time.Date(2026, 9, 22, 16, 0, 0, 0, time.UTC)
	if !job.StartedAt.Equal(wantStarted) || !job.CompletedAt.IsZero() {
		t.Fatalf("unexpected job timing: %+v", job)
	}
	if job.Steps[1].Name != "Run tests" || job.Steps[1].Status != "in_progress" {
		t.Fatalf("unexpected steps: %+v", job.Steps)
	}
}
