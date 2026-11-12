package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/mcpaccess"
)

func TestMCPPairingDashboardApprovalAndRevocation(t *testing.T) {
	registry, err := mcpaccess.New(filepath.Join(t.TempDir(), "mcp_access.json"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := auth.New(auth.Config{
		APIKeys: auth.ParseKeys("admin-secret:admin:admin"),
		PairedAuthenticator: func(token string) (auth.Principal, bool) {
			name, scope, ok := registry.Lookup(token)
			if !ok {
				return auth.Principal{}, false
			}
			role := auth.RoleViewer
			if scope == "operator" {
				role = auth.RoleOperator
			}
			return auth.Principal{Name: name, Role: role, MCP: true}, true
		},
	})
	svc := &Service{MCP: registry, Auth: mgr}
	h := svc.Handler()
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	makeRequest := func(method, path string, body []byte, authorization, origin, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://runnerhub.test"+path, bytes.NewReader(body))
		r.RemoteAddr = "10.0.0.9:2345"
		if authorization != "" {
			r.Header.Set("Authorization", "Bearer "+authorization)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	pairData, _ := json.Marshal(map[string]string{"name": "Codex Desktop", "token": token})
	res := makeRequest("POST", "/api/v1/mcp/pair/request", pairData, "", "", "application/json")
	if res.Code != 200 || registry.Status(token) != "pending" {
		t.Fatalf("pair status=%d %s", res.Code, res.Body.String())
	}
	res = makeRequest("GET", "/mcp/connections", nil, "", "", "")
	if res.Code != 401 {
		t.Fatalf("unauthenticated page=%d", res.Code)
	}
	res = makeRequest("GET", "/mcp/connections", nil, token, "", "")
	if res.Code != 401 {
		t.Fatalf("unapproved client accessed dashboard: %d", res.Code)
	}
	res = makeRequest("GET", "/mcp/connections", nil, "admin-secret", "", "")
	if res.Code != 200 || !strings.Contains(res.Body.String(), "Codex Desktop") {
		t.Fatalf("admin dashboard=%d %s", res.Code, res.Body.String())
	}
	id := registry.List()[0].RequestID
	form := url.Values{"id": {id}, "action": {"approve-viewer"}}.Encode()
	res = makeRequest("POST", "/mcp/connections", []byte(form), "admin-secret", "https://attacker.test", "application/x-www-form-urlencoded")
	if res.Code != 403 || registry.Status(token) != "pending" {
		t.Fatal("CSRF approval was accepted")
	}
	res = makeRequest("POST", "/mcp/connections", []byte(form), "admin-secret", "http://runnerhub.test", "application/x-www-form-urlencoded")
	if res.Code != 303 || registry.Status(token) != "approved" {
		t.Fatalf("admin approval=%d", res.Code)
	}
	res = makeRequest("GET", "/mcp/connections", nil, token, "", "")
	if res.Code != 403 {
		t.Fatalf("paired approved client accessed admin dashboard: %d", res.Code)
	}
	res = makeRequest("GET", "/api/v1/audit", nil, token, "", "")
	if res.Code != 403 {
		t.Fatalf("viewer accessed restricted endpoint: %d", res.Code)
	}
	id = registry.List()[0].RequestID
	form = url.Values{"id": {id}, "action": {"revoke"}}.Encode()
	res = makeRequest("POST", "/mcp/connections", []byte(form), "admin-secret", "http://runnerhub.test", "application/x-www-form-urlencoded")
	if res.Code != 303 || registry.Status(token) != "unregistered" {
		t.Fatalf("revocation=%d", res.Code)
	}
}

func TestMCPRequestCannotEscalateBySupplyingScope(t *testing.T) {
	registry, err := mcpaccess.New(filepath.Join(t.TempDir(), "mcp_access.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{MCP: registry, Auth: auth.New(auth.Config{APIKeys: auth.ParseKeys("admin:admin")})}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	body, _ := json.Marshal(map[string]string{"name": "requester", "token": token, "scope": "operator"})
	req := httptest.NewRequest("POST", "/api/v1/mcp/pair/request", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("request failed: %d %s", rec.Code, rec.Body.String())
	}
	entries := registry.List()
	if len(entries) != 1 || entries[0].Approved || entries[0].Scope != "" {
		t.Fatalf("client specified its own privileges: %+v", entries)
	}
}
