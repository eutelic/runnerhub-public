package githubrunner

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
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

func TestGitHubAppTokenSourceResolvesAndCachesRepositoryInstallation(t *testing.T) {
	var installationRequests atomic.Int32
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Fatal("expected bearer jwt")
		}
		switch r.URL.Path {
		case "/repos/eutelic/runnerhub/installation":
			installationRequests.Add(1)
			_, _ = w.Write([]byte(`{"id":77}`))
		case "/app/installations/77/access_tokens":
			tokenRequests.Add(1)
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
	if installationRequests.Load() != 1 || tokenRequests.Load() != 1 {
		t.Fatalf("expected one installation and token request, got %d and %d", installationRequests.Load(), tokenRequests.Load())
	}
}

func TestGitHubAppJWTShape(t *testing.T) {
	source, err := NewGitHubAppTokenSource(GitHubAppConfig{
		Issuer:        "Iv1.client-id",
		PrivateKeyPEM: testPrivateKeyPEM(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	source.now = func() time.Time { return now }
	jwt, err := source.appJWT()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		t.Fatalf("expected three-part signed jwt, got %q", jwt)
	}
}

func TestGitHubAppTokenSourceRejectsNonPEMKey(t *testing.T) {
	_, err := NewGitHubAppTokenSource(GitHubAppConfig{Issuer: "app", PrivateKeyPEM: []byte("not pem")})
	if err == nil {
		t.Fatal("expected invalid key to fail")
	}
}
