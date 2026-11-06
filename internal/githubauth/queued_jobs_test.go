package githubauth

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func queuedJobsTestManager(t *testing.T, private bool, transport http.RoundTripper) (*Manager, *http.Request) {
	t.Helper()
	m, err := New(Config{
		ClientID: "Iv1.test", ClientSecret: "secret", AppSlug: "runnerhub-test",
		PublicURL: "http://runnerhub.example.test:8787", APIURL: "https://api.github.test",
		StateDir: t.TempDir(), HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil { t.Fatal(err) }
	s := session{Login: "example-user", Token: "user-token", CSRF: "csrf",
		ExpiresAt: time.Now().Add(time.Hour)}
	m.sessions["sid"] = s
	m.repositoriesCache[discoveryKey(s)] = repositoryCacheEntry{
		Items: []ConnectedRepository{
			{FullName: "eutelic/private", Private: private, InstallationID: 42},
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	r := httptest.NewRequest(http.MethodGet, "http://runnerhub.example.test:8787/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sid"})
	return m, r
}

func TestQueuedJobsReadOnlyAndScopedToVisiblePrivateRepo(t *testing.T) {
	calls := 0
	m, r := queuedJobsTestManager(t, true, roundTripFunc(func(req *http.Request) (*http.Response,error) {
		calls++
		if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer user-token" {
			t.Errorf("unexpected GitHub request %s", req.Method)
		}
		var body string
		switch {
		case strings.Contains(req.URL.RawQuery, "status=queued"):
			body = `{"total_count":1,"workflow_runs":[{"id":9,"html_url":"https://github.com/eutelic/private/actions/runs/9"}]}`
		case strings.Contains(req.URL.Path, "/runs/9/jobs"):
			body = `{"total_count":2,"jobs":[{"id":11,"name":"build","status":"queued","labels":["runnerhub-publisher"],"html_url":"https://github.com/eutelic/private/actions/runs/9/job/11"},{"id":12,"status":"completed"}]}`
		case strings.Contains(req.URL.RawQuery, "status=in_progress"):
			body = `{"total_count":0,"workflow_runs":[]}`
		default:
			t.Errorf("unexpected endpoint %s", req.URL)
			body = `{}`
		}
		return &http.Response{StatusCode:200,Header:make(http.Header),
			Body:io.NopCloser(strings.NewReader(body)),Request:req},nil
	}))
	report,err:=m.QueuedJobs(r,"EUTELIC/private")
	if err!=nil {t.Fatal(err)}
	if report.Repository!="eutelic/private" || report.CheckedRuns!=1 || len(report.Jobs)!=1 ||
		report.Jobs[0].ID!=11 || len(report.Jobs[0].Labels)!=1 ||
		report.Jobs[0].Labels[0]!="runnerhub-publisher" {
		t.Fatalf("unexpected report: %+v",report)
	}
	if calls!=3 {t.Fatalf("expected 3 bounded GitHub calls; got %d",calls)}
}

func TestQueuedJobsRejectsUninstalledAndPublicRepositories(t *testing.T) {
	calls:=0
	transport:=roundTripFunc(func(req *http.Request)(*http.Response,error){
		calls++
		return &http.Response{StatusCode:500,Header:make(http.Header),
			Body:io.NopCloser(strings.NewReader("unexpected network request")),Request:req},nil
	})
	m,r:=queuedJobsTestManager(t,true,transport)
	if _,err:=m.QueuedJobs(r,"eutelic/not-installed");err==nil {
		t.Fatal("uninstalled repo must be refused")
	}
	m2,r2:=queuedJobsTestManager(t,false,transport)
	if _,err:=m2.QueuedJobs(r2,"eutelic/private");err==nil {
		t.Fatal("public repo must be refused")
	}
	if calls!=0 {t.Fatalf("unauthorized repository triggered %d GitHub calls",calls)}
}

func TestQueuedJobsMarksTruncatedAndIgnoresCompletedJobs(t *testing.T) {
	m,r:=queuedJobsTestManager(t,true,roundTripFunc(func(req *http.Request)(*http.Response,error){
		body:=`{"total_count":0,"workflow_runs":[]}`
		switch{
		case strings.Contains(req.URL.RawQuery,"status=queued"):
			body=`{"total_count":12,"workflow_runs":[{"id":7,"html_url":"https://github.com/eutelic/private/actions/runs/7"}]}`
		case strings.Contains(req.URL.Path,"/runs/7/jobs"):
			body=`{"total_count":101,"jobs":[{"id":1,"status":"completed"},{"id":2,"status":"waiting","labels":["runnerhub"]}]}`
		}
		return &http.Response{StatusCode:200,Header:make(http.Header),
			Body:io.NopCloser(strings.NewReader(body)),Request:req},nil
	}))
	report,err:=m.QueuedJobs(r,"eutelic/private")
	if err!=nil {t.Fatal(err)}
	if !report.Truncated || len(report.Jobs)!=1 || report.Jobs[0].Status!="waiting" {
		t.Fatalf("unexpected bounded report: %+v",report)
	}
}

func TestQueuedJobsClassifiesSanitizedGitHubFailures(t *testing.T) {
	cases := []struct {
		name string
		status int
		message string
		remaining string
		code string
	}{
		{"missing Actions permission", 403, "Resource not accessible by integration", "100", "github-actions-permission"},
		{"primary rate limit", 403, "Resource not accessible by integration", "0", "github-rate-limited"},
		{"secondary rate limit", 403, "You have exceeded a secondary rate limit", "100", "github-rate-limited"},
		{"HTTP 429 throttling", 429, "Too many requests", "100", "github-rate-limited"},
		{"expired OAuth token", 401, "Bad credentials", "100", "github-auth-expired"},
		{"inaccessible repository", 404, "Not Found", "100", "github-repository-inaccessible"},
		{"other forbidden", 403, "Forbidden", "100", "github-forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			m,r := queuedJobsTestManager(t,true,roundTripFunc(func(req *http.Request) (*http.Response,error) {
				calls++
				header := make(http.Header)
				header.Set("X-GitHub-Request-Id","BEEF:1")
				header.Set("X-RateLimit-Remaining",tc.remaining)
				header.Set("X-RateLimit-Reset","1794013200")
				return &http.Response{
					StatusCode:tc.status,Header:header,
					Body:io.NopCloser(strings.NewReader(`{"message":`+strconv.Quote(tc.message)+`,"token":"do-not-leak"}`)),
					Request:req,
				},nil
			}))
			_,err := m.QueuedJobs(r,"eutelic/private")
			var classified *QueueInspectionError
			if !errors.As(err,&classified) {
				t.Fatalf("expected typed queue error, got %T: %v",err,err)
			}
			if classified.Code!=tc.code || classified.RequestID!="BEEF:1" {
				t.Fatalf("code=%q id=%q, want %q/BEEF:1",classified.Code,classified.RequestID,tc.code)
			}
			if tc.code=="github-rate-limited" && classified.RetryAt=="" {
				t.Fatal("rate-limit reset timestamp was not retained")
			}
			if strings.Contains(err.Error(),"do-not-leak") || strings.Contains(err.Error(),tc.message) {
				t.Fatalf("raw GitHub response leaked to caller: %v",err)
			}
			if calls!=1 {t.Fatalf("expected one GitHub request, got %d",calls)}
		})
	}
}
