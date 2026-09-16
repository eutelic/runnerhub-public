package githubrunner

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func testPrivateKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func jwtClaims(t *testing.T, raw string) map[string]any {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("expected three-part jwt, got %q", raw)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestGitHubAppTokenSourceResolvesClientIDToNumericAppIDAndCaches(t *testing.T) {
	var appRequests atomic.Int32
	var installationRequests atomic.Int32
	var tokenRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apps/runnerhub-self-hosted-ci":
			appRequests.Add(1)
			if r.Header.Get("Authorization") != "" {
				t.Fatal("public app metadata resolution unexpectedly sent authorization")
			}
			_, _ = w.Write([]byte(`{"id":4242,"client_id":"Iv1.client-id","slug":"runnerhub-self-hosted-ci"}`))
		case "/repos/eutelic/runnerhub/installation":
			installationRequests.Add(1)
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				t.Fatal("expected bearer jwt")
			}
			claims := jwtClaims(t, strings.TrimPrefix(auth, "Bearer "))
			if got := claims["iss"]; got != float64(4242) {
				t.Fatalf("jwt iss=%#v, want numeric app id 4242", got)
			}
			_, _ = w.Write([]byte(`{"id":77}`))
		case "/app/installations/77/access_tokens":
			tokenRequests.Add(1)
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				t.Fatal("expected bearer jwt")
			}
			claims := jwtClaims(t, strings.TrimPrefix(auth, "Bearer "))
			if got := claims["iss"]; got != float64(4242) {
				t.Fatalf("token jwt iss=%#v, want numeric app id 4242", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			repositories, ok := body["repositories"].([]any)
			if !ok || len(repositories) != 1 || repositories[0] != "runnerhub" {
				t.Fatalf("repository token was not narrowed: %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"ghs_new_format_token","expires_at":"2030-01-01T00:00:00Z"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	source, err := NewGitHubAppTokenSource(GitHubAppConfig{
		Issuer:        "Iv1.client-id",
		AppSlug:       "runnerhub-self-hosted-ci",
		PrivateKeyPEM: testPrivateKeyPEM(t),
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	source.now = func() time.Time { return time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC) }
	scope := core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"}

	first, err := source.Token(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Token(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if first != "ghs_new_format_token" || second != first {
		t.Fatalf("unexpected token values %q %q", first, second)
	}
	if appRequests.Load() != 1 || installationRequests.Load() != 1 || tokenRequests.Load() != 1 {
		t.Fatalf("expected one app/install/token request, got %d/%d/%d", appRequests.Load(), installationRequests.Load(), tokenRequests.Load())
	}
}

func TestGitHubAppJWTUsesNumericConfiguredIssuerAsJSONNumber(t *testing.T) {
	source, err := NewGitHubAppTokenSource(GitHubAppConfig{
		Issuer:        "4242",
		PrivateKeyPEM: testPrivateKeyPEM(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	source.now = func() time.Time { return now }
	raw, err := source.appJWT(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claims := jwtClaims(t, raw)
	if got := claims["iss"]; got != float64(4242) {
		t.Fatalf("iss=%#v, want numeric 4242", got)
	}
}

func TestGitHubAppJWTKeepsClientIDWithoutSlug(t *testing.T) {
	source, err := NewGitHubAppTokenSource(GitHubAppConfig{
		Issuer:        "Iv1.client-id",
		PrivateKeyPEM: testPrivateKeyPEM(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := source.appJWT(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claims := jwtClaims(t, raw)
	if got := claims["iss"]; got != "Iv1.client-id" {
		t.Fatalf("iss=%#v, want configured client id", got)
	}
}

func TestGitHubAppClientIDResolutionRejectsMismatchedSlug(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apps/wrong-app" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":99,"client_id":"Iv1.other","slug":"wrong-app"}`))
	}))
	defer server.Close()

	source, err := NewGitHubAppTokenSource(GitHubAppConfig{
		Issuer:        "Iv1.expected",
		AppSlug:       "wrong-app",
		PrivateKeyPEM: testPrivateKeyPEM(t),
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.appJWT(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not configured client id") {
		t.Fatalf("expected client-id mismatch, got %v", err)
	}
}

func TestGitHubAppClientIDResolutionReportsLookupFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	source, err := NewGitHubAppTokenSource(GitHubAppConfig{
		Issuer:        "Iv1.expected",
		AppSlug:       "missing-app",
		PrivateKeyPEM: testPrivateKeyPEM(t),
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.appJWT(context.Background())
	if err == nil || !strings.Contains(err.Error(), "returned 404") {
		t.Fatalf("expected lookup failure, got %v", err)
	}
}

func TestGitHubAppTokenSourceRejectsNonPEMKey(t *testing.T) {
	_, err := NewGitHubAppTokenSource(GitHubAppConfig{Issuer: "app", PrivateKeyPEM: []byte("not pem")})
	if err == nil {
		t.Fatal("expected invalid key to fail")
	}
}
