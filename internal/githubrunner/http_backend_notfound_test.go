package githubrunner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/eutelic/runnerhub/internal/core"
)
func TestHTTPBackendMapsRunner404(t *testing.T){s:=httptest.NewServer(http.NotFoundHandler());defer s.Close();b,err:=NewHTTPBackendWithBaseURL(s.Client(),staticTokenSource("secret"),s.URL);if err!=nil{t.Fatal(err)};_,err=b.GetRunner(context.Background(),core.GitHubScope{Kind:core.ScopeRepository,Owner:"o",Repository:"r"},1);if !errors.Is(err,ErrNotFound){t.Fatalf("expected ErrNotFound, got %v",err)}}
