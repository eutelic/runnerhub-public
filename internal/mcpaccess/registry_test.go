package mcpaccess

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testToken(n byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n}, 32))
}

func TestPairApprovePersistRevokeAndScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_access.json")
	r, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	token := testToken(1)
	if r.Status(token) != "unregistered" {
		t.Fatal("new client must not be approved")
	}
	status, err := r.Request(token, "Codex Linux", "127.0.0.1")
	if err != nil || status != "pending" {
		t.Fatalf("request: %q %v", status, err)
	}
	entries := r.List()
	if len(entries) != 1 {
		t.Fatalf("entries=%v", entries)
	}
	if _, _, ok := r.Lookup(token); ok {
		t.Fatal("pending key authorized")
	}
	if err := r.Decide(entries[0].RequestID, "approve-viewer"); err != nil {
		t.Fatal(err)
	}
	if name, scope, ok := r.Lookup(token); !ok || name != "Codex Linux" || scope != "viewer" {
		t.Fatalf("grant=%q %q %v", name, scope, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(token)) {
		t.Fatal("plaintext client secret persisted")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatal("grant file is not private")
	}
	again, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status(token) != "approved" {
		t.Fatal("grant not persisted")
	}
	if err := again.Decide(again.List()[0].RequestID, "revoke"); err != nil {
		t.Fatal(err)
	}
	if r2, err := New(path); err != nil || r2.Status(token) != "unregistered" {
		t.Fatalf("revocation not persistent: %v", err)
	}
}

func TestPendingExpirationAndOperatorGrant(t *testing.T) {
	r, err := New(filepath.Join(t.TempDir(), "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	token := testToken(2)
	if _, err := r.Request(token, "Publisher", "10.0.0.3"); err != nil {
		t.Fatal(err)
	}
	id := r.List()[0].RequestID
	r.mu.Lock()
	e := r.entries[tokenDigest(token)]
	e.Expires = time.Now().Add(-time.Second)
	r.entries[tokenDigest(token)] = e
	r.mu.Unlock()
	if r.Status(token) != "unregistered" {
		t.Fatal("expired pending request valid")
	}
	if err := r.Decide(id, "approve-operator"); err == nil {
		t.Fatal("approved expired request")
	}
	if _, err := r.Request(token, "Publisher", "10.0.0.3"); err != nil {
		t.Fatal(err)
	}
	if err := r.Decide(r.List()[0].RequestID, "approve-operator"); err != nil {
		t.Fatal(err)
	}
	if _, scope, ok := r.Lookup(token); !ok || scope != "operator" {
		t.Fatalf("operator=%q %v", scope, ok)
	}
}

func TestRejectBadTokensAndThrottleRequests(t *testing.T) {
	r, err := New(filepath.Join(t.TempDir(), "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"tiny", strings.Repeat(" ", 43), strings.Repeat("!", 43)} {
		if _, err := r.Request(token, "test", "local"); err == nil {
			t.Fatal("accepted invalid token")
		}
	}
	for i := byte(10); i < 18; i++ {
		if _, err := r.Request(testToken(i), "tester", "local"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Request(testToken(18), "tester", "local"); err == nil {
		t.Fatal("rate limit not enforced")
	}
}
