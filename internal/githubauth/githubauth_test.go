package githubauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if err != nil {
		t.Fatal(err)
	}
	m.sessions["sid"] = session{Login: "example-user", Token: "token", CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour)}

	get := httptest.NewRequest(http.MethodGet, "http://runnerhub.example.test:8787/api/v1/hosts", nil)
	get.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sid"})
	if p, ok := m.Authenticate(get); !ok || p.Name != "github:example-user" {
		t.Fatalf("GET browser session auth=%+v ok=%v", p, ok)
	}

	post := httptest.NewRequest(http.MethodPost, "http://runnerhub.example.test:8787/api/v1/github/connect", nil)
	post.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sid"})
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
	m, err := New(Config{ClientID: "Iv1.test", ClientSecret: "secret", AppSlug: "runnerhub-test", PublicURL: "http://runnerhub.example.test:8787", StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.claimOwner("example-user"); err != nil {
		t.Fatal(err)
	}
	if err := m.claimOwner("example-user"); err != nil {
		t.Fatalf("same owner case-insensitive rejected: %v", err)
	}
	if err := m.claimOwner("other-user"); err == nil {
		t.Fatal("second GitHub owner unexpectedly accepted")
	}
}

func TestSessionsPersistAcrossManagerRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{ClientID: "Iv1.test", ClientSecret: "secret", AppSlug: "runnerhub-test", PublicURL: "http://runnerhub.example.test:8787", StateDir: dir}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.sessions["sid"] = session{Login: "example-user", Token: "token", CSRF: "csrf", ExpiresAt: time.Now().Add(24 * time.Hour)}
	if err := m.persistSessionsLocked(); err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	m.mu.Unlock()

	info, err := os.Stat(filepath.Join(dir, sessionsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sessions file mode=%o want 600", info.Mode().Perm())
	}

	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://runnerhub.example.test:8787/api/v1/hosts", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sid"})
	if p, ok := restarted.Authenticate(req); !ok || p.Name != "github:example-user" {
		t.Fatalf("persisted session auth=%+v ok=%v", p, ok)
	}
}

func TestExpiredGitHubAccessTokenRefreshesAndPersists(t *testing.T) {
	dir := t.TempDir()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != "https://github.com/login/oauth/access_token" {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"error":"not found"}`)), Header: make(http.Header), Request: r}, nil
		}
		body := `{"access_token":"new-token","expires_in":28800,"refresh_token":"new-refresh","refresh_token_expires_in":15897600}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	cfg := Config{ClientID: "Iv1.test", ClientSecret: "secret", AppSlug: "runnerhub-test", PublicURL: "http://runnerhub.example.test:8787", StateDir: dir, HTTPClient: client}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.sessions["sid"] = session{
		Login: "example-user", Token: "old-token", RefreshToken: "old-refresh", CSRF: "csrf",
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour), TokenExpiresAt: time.Now().Add(-time.Minute), RefreshExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}
	if err := m.persistSessionsLocked(); err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	m.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "http://runnerhub.example.test:8787/api/v1/hosts", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sid"})
	if _, ok := m.Authenticate(req); !ok {
		t.Fatal("session was not authenticated after token refresh")
	}
	m.mu.Lock()
	got := m.sessions["sid"]
	m.mu.Unlock()
	if got.Token != "new-token" || got.RefreshToken != "new-refresh" || !got.TokenExpiresAt.After(time.Now()) {
		t.Fatalf("unexpected refreshed session: %+v", got)
	}

	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	restarted.mu.Lock()
	persisted := restarted.sessions["sid"]
	restarted.mu.Unlock()
	if persisted.Token != "new-token" || persisted.RefreshToken != "new-refresh" {
		t.Fatalf("refreshed credentials were not persisted: %+v", persisted)
	}
}

func TestNonExpiringGitHubTokenHasNoSyntheticEightHourExpiry(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"access_token":"non-expiring-token"}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	m, err := New(Config{
		ClientID: "Iv1.test", ClientSecret: "secret", AppSlug: "runnerhub-test",
		PublicURL: "http://runnerhub.example.test:8787", StateDir: t.TempDir(), HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://runnerhub.example.test:8787/auth/github/callback", nil)
	tokens, err := m.exchangeCode(req, "code")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "non-expiring-token" || !tokens.AccessExpiresAt.IsZero() {
		t.Fatalf("unexpected non-expiring token metadata: %+v", tokens)
	}
}

func TestAccessibleInstallationsIncludePersonalAndOrganizationAccounts(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/user/installations" {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"error":"not found"}`)), Header: make(http.Header), Request: r}, nil
		}
		body := `{"installations":[{"id":41,"account":{"login":"example-user","type":"User"}},{"id":42,"account":{"login":"eutelic","type":"Organization"}}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	m, err := New(Config{
		ClientID: "Iv1.test", ClientSecret: "secret", AppSlug: "runnerhub-test",
		PublicURL: "http://runnerhub.example.test:8787", APIURL: "https://api.github.test", StateDir: t.TempDir(), HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.sessions["sid"] = session{Login: "example-user", Token: "user-token", CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour)}
	req := httptest.NewRequest(http.MethodGet, "http://runnerhub.example.test:8787/api/v1/github/installations", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sid"})
	installations, err := m.AccessibleInstallations(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 2 {
		t.Fatalf("installations=%#v", installations)
	}
	if installations[0].Account != "example-user" || installations[0].AccountType != "User" {
		t.Fatalf("personal installation=%#v", installations[0])
	}
	if installations[1].Account != "eutelic" || installations[1].AccountType != "Organization" {
		t.Fatalf("organization installation=%#v", installations[1])
	}
}

func TestConnectedRepositoriesComeFromAccessibleInstallations(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch {
		case r.URL.Path == "/user/installations":
			body = `{"installations":[{"id":42,"account":{"login":"eutelic","type":"Organization"}}]}`
		case r.URL.Path == "/user/installations/42/repositories":
			body = `{"repositories":[{"id":7,"full_name":"eutelic/runnerhub","private":true,"default_branch":"main"}]}`
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"error":"not found"}`)), Header: make(http.Header), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	m, err := New(Config{
		ClientID: "Iv1.test", ClientSecret: "secret", AppSlug: "runnerhub-test",
		PublicURL: "http://runnerhub.example.test:8787", APIURL: "https://api.github.test", StateDir: t.TempDir(), HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.sessions["sid"] = session{Login: "example-user", Token: "user-token", CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour)}
	req := httptest.NewRequest(http.MethodGet, "http://runnerhub.example.test:8787/api/v1/github/connected-repositories", nil)
	req = req.WithContext(context.Background())
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sid"})
	repos, err := m.ConnectedRepositories(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].FullName != "eutelic/runnerhub" || repos[0].InstallationID != 42 || repos[0].Account != "eutelic" || repos[0].AccountType != "Organization" || !repos[0].Private {
		t.Fatalf("unexpected repositories: %#v", repos)
	}
}

func TestDiscoveryCacheSharesInstallationsAcrossDashboardRequests(t *testing.T) {
	installationCalls := 0
	repositoryCalls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch {
		case r.URL.Path == "/user/installations":
			installationCalls++
			body = `{"installations":[{"id":42,"account":{"login":"eutelic","type":"Organization"}}]}`
		case r.URL.Path == "/user/installations/42/repositories":
			repositoryCalls++
			body = `{"repositories":[{"id":7,"full_name":"eutelic/runnerhub","private":true,"default_branch":"main"}]}`
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"error":"not found"}`)), Header: make(http.Header), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	m, err := New(Config{
		ClientID: "Iv1.test", ClientSecret: "secret", AppSlug: "runnerhub-test",
		PublicURL: "http://runnerhub.example.test:8787", APIURL: "https://api.github.test", StateDir: t.TempDir(), HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.sessions["sid"] = session{Login: "example-user", Token: "user-token", CSRF: "csrf", ExpiresAt: time.Now().Add(time.Hour)}
	req := httptest.NewRequest(http.MethodGet, "http://runnerhub.example.test:8787/api/v1/github/installations", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sid"})

	if _, err := m.AccessibleInstallations(req); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ConnectedRepositories(req); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AccessibleInstallations(req); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ConnectedRepositories(req); err != nil {
		t.Fatal(err)
	}

	if installationCalls != 1 {
		t.Fatalf("installation API calls=%d, want 1", installationCalls)
	}
	if repositoryCalls != 1 {
		t.Fatalf("repository API calls=%d, want 1", repositoryCalls)
	}
}
