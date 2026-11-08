package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func signed(t *testing.T, priv ed25519.PrivateKey, m Manifest) Manifest {
	t.Helper()
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, m.signedPayload()))
	return m
}
func TestManifestSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := signed(t, priv, Manifest{Version: "1.2.3", URL: "https://example.invalid/a", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err := VerifyManifest(m, pub); err != nil {
		t.Fatal(err)
	}
	m.Version = "evil"
	if VerifyManifest(m, pub) == nil {
		t.Fatal("tamper accepted")
	}
}
func TestStageUsesVerifiedAtomicArtifact(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("runnerhub-binary")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer srv.Close()
	m := signed(t, priv, Manifest{Version: "1.0.0", URL: srv.URL, SHA256: hex.EncodeToString(sum[:])})
	dst := filepath.Join(t.TempDir(), "runnerhub-agent")
	if err := (Stager{HTTP: srv.Client(), PublicKey: pub}).Stage(m, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("got=%q err=%v", got, err)
	}
}
func TestManifestRejectsBadDigestEncoding(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := Manifest{Version: "1", URL: "https://example.invalid/a", SHA256: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}
	m = signed(t, priv, m)
	if VerifyManifest(m, pub) == nil {
		t.Fatal("invalid digest accepted")
	}
}
