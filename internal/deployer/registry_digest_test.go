package deployer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const registryTestImage = "ghcr.io/eutelic/file-ingest:latest"
const registryTestDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

func TestGHCRDigestResolverAuthenticatesOnlySameRegistry(t *testing.T) {
	var authRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			authRequests++
			if r.URL.Query().Get("scope") != "repository:eutelic/file-ingest:pull" ||
				r.URL.Query().Get("service") != "ghcr.io" {
				t.Errorf("unexpected GHCR token request: %s", r.URL.String())
			}
			fmt.Fprint(w, `{"token":"test-pull-token"}`)
			return
		}
		if r.URL.Path != "/v2/eutelic/file-ingest/manifests/latest" ||
			r.Method != http.MethodHead || !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-pull-token" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="ghcr.io",scope="repository:eutelic/file-ingest:pull"`, "http://"+r.Host))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Docker-Content-Digest", registryTestDigest)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	resolver := GHCRDigestResolver{BaseURL: server.URL, Client: server.Client()}
	got, err := resolver.Digest(context.Background(), registryTestImage)
	if err != nil || got != registryTestDigest || authRequests != 1 {
		t.Fatalf("got=%q tokenRequests=%d err=%v", got, authRequests, err)
	}
}

func TestGHCRDigestResolverRejectsUntrustedChallenge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="https://attacker.invalid/token",service="ghcr.io",scope="repository:eutelic/file-ingest:pull"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	resolver := GHCRDigestResolver{BaseURL: server.URL, Client: server.Client()}
	if _, err := resolver.Digest(context.Background(), registryTestImage); err == nil {
		t.Fatal("accepted credential-fetch redirect to another registry")
	}
}

func TestGHCRDigestResolverRequiresValidManifestDigest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	resolver := GHCRDigestResolver{BaseURL: server.URL, Client: server.Client()}
	if _, err := resolver.Digest(context.Background(), registryTestImage); err == nil {
		t.Fatal("accepted truncated OCI image digest")
	}
	for _, image := range []string{
		"https://attacker.invalid/latest",
		"ghcr.io/eutelic/file-ingest:evil",
		"ghcr.io/eutelic/../file-ingest:latest",
		"ghcr.io/eutelic/file-ingest:sha-deadbeef",
	} {
		if _, err := resolver.Digest(context.Background(), image); err == nil {
			t.Fatalf("unsafe GHCR reference accepted: %s", image)
		}
	}
}
