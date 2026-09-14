package githubauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSessionAuthenticationRequiresCSRFForMutation(t *testing.T) {
	m, err := New(Config{
		ClientID: "Iv1.test", ClientSecret: "secret", AppSlug: "runnerhub-test",
		PublicURL: "http://runnerhub.example.test:8787", StateDir: t.TempDir(),
	})
	if err != nil { t.Fatal(err) }
	m.sessions["sid"] = session{Login:"example-user",Token:"token",CSRF:"csrf",ExpiresAt:time.Now().Add(time.Hour)}

	get := httptest.NewRequest(http.MethodGet, "http://runnerhub.example.test:8787/api/v1/hosts", nil)
	get.AddCookie(&http.Cookie{Name:sessionCookieName,Value:"sid"})
	if p, ok := m.Authenticate(get); !ok || p.Name != "github:example-user" {
		t.Fatalf("GET browser session auth=%+v ok=%v", p, ok)
	}

	post := httptest.NewRequest(http.MethodPost, "http://runnerhub.example.test:8787/api/v1/github/connect", nil)
	post.AddCookie(&http.Cookie{Name:sessionCookieName,Value:"sid"})
	if _, ok := m.Authenticate(post); ok {
		t.Fatal("mutation without CSRF was authenticated")
	}
	post.Header.Set("X-RunnerHub-CSRF", "csrf")
	if _, ok := m.Authenticate(post); !ok {
		t.Fatal("mutation with CSRF was rejected")
	}
}

func TestClaimOwnerPersistsSingleGitHubOwner(t *testing.T) {
	dir := t.TempDir()
	m, err := New(Config{ClientID:"Iv1.test",ClientSecret:"secret",AppSlug:"runnerhub-test",PublicURL:"http://runnerhub.example.test:8787",StateDir:dir})
	if err != nil { t.Fatal(err) }
	if err := m.claimOwner("example-user"); err != nil { t.Fatal(err) }
	if err := m.claimOwner("example-user"); err != nil { t.Fatalf("same owner case-insensitive rejected: %v", err) }
	if err := m.claimOwner("other-user"); err == nil {
		t.Fatal("second GitHub owner unexpectedly accepted")
	}
}

func TestConnectedRepositoriesComeFromAccessibleInstallations(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response,error) {
		body := ""
		switch {
		case r.URL.Path == "/user/installations":
			body = `{"installations":[{"id":42,"account":{"login":"eutelic"}}]}`
		case r.URL.Path == "/user/installations/42/repositories":
			body = `{"repositories":[{"id":7,"full_name":"eutelic/runnerhub","private":true,"default_branch":"main"}]}`
		default:
			return &http.Response{StatusCode:404,Body:io.NopCloser(strings.NewReader(`{"error":"not found"}`)),Header:make(http.Header),Request:r},nil
		}
		return &http.Response{StatusCode:200,Body:io.NopCloser(strings.NewReader(body)),Header:make(http.Header),Request:r},nil
	})}
	m, err := New(Config{
		ClientID:"Iv1.test",ClientSecret:"secret",AppSlug:"runnerhub-test",
		PublicURL:"http://runnerhub.example.test:8787",APIURL:"https://api.github.test",StateDir:t.TempDir(),HTTPClient:client,
	})
	if err != nil { t.Fatal(err) }
	m.sessions["sid"] = session{Login:"example-user",Token:"user-token",CSRF:"csrf",ExpiresAt:time.Now().Add(time.Hour)}
	req := httptest.NewRequest(http.MethodGet, "http://runnerhub.example.test:8787/api/v1/github/connected-repositories", nil)
	req = req.WithContext(context.Background())
	req.AddCookie(&http.Cookie{Name:sessionCookieName,Value:"sid"})
	repos, err := m.ConnectedRepositories(req)
	if err != nil { t.Fatal(err) }
	if len(repos)!=1 || repos[0].FullName!="eutelic/runnerhub" || repos[0].InstallationID!=42 || !repos[0].Private {
		t.Fatalf("unexpected repositories: %#v", repos)
	}
}
