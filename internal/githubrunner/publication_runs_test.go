package githubrunner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestLatestPublicationRunUsesConfiguredWorkflowAndBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/eutelic/file-ingest/actions/workflows/publish-images.yml/runs" ||
			r.URL.Query().Get("branch") != "main" || r.URL.Query().Get("per_page") != "1" {
			t.Errorf("unexpected publication request: %s", r.URL.String())
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"workflow_runs":[{"id":123,"run_attempt":1,"name":"publish","path":".github/workflows/publish-images.yml@main","event":"workflow_dispatch","status":"completed","conclusion":"success","head_branch":"main","head_sha":"0123456789abcdef0123456789abcdef01234567"}]}`)
	}))
	defer server.Close()
	backend, err := NewHTTPBackendWithBaseURL(server.Client(), staticTokenSource("token"), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	scope := core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "file-ingest"}
	run, ok, err := backend.LatestPublicationRun(context.Background(), scope, "publish-images.yml", "main")
	if err != nil || !ok || run.ID != 123 || run.Status != "completed" || run.Conclusion != "success" {
		t.Fatalf("publication=%+v found=%t err=%v", run, ok, err)
	}
	if _, _, err := backend.LatestPublicationRun(context.Background(), scope, "../ci.yml", "main"); err == nil {
		t.Fatal("accepted path traversal")
	}
	if _, _, err := backend.LatestPublicationRun(context.Background(), scope, "publish-images.yml", "feature"); err == nil {
		t.Fatal("accepted unsupported branch")
	}
}
