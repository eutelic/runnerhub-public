package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPairedCredentialsHaveHardAPIAllowlist(t *testing.T) {
	m := New(Config{
		APIKeys: ParseKeys("admin-key:admin:real-admin"),
		PairedAuthenticator: func(token string) (Principal, bool) {
			switch token {
			case "viewer":
				return Principal{Name: "mcp:viewer", Role: RoleViewer, MCP: true}, true
			case "operator":
				return Principal{Name: "mcp:operator", Role: RoleOperator, MCP: true}, true
			}
			return Principal{}, false
		},
	})
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	cases := []struct {
		token, method, path string
		want                int
	}{
		{"viewer", "GET", "/api/v1/runtime-health", 204},
		{"viewer", "GET", "/api/v1/workers/history", 204},
		{"viewer", "GET", "/api/v1/pools/pool-1/health", 204},
		{"viewer", "POST", "/api/v1/pools/pool-1/workers", 403},
		{"viewer", "GET", "/api/v1/audit", 403},
		{"operator", "GET", "/api/v1/fleet/versions", 204},
		{"operator", "POST", "/api/v1/pools/pool-1/workers", 204},
		{"operator", "POST", "/api/v1/pools/pool-1/scale-workers", 403},
		{"operator", "POST", "/api/v1/deployer/apps/demo/redeploy", 403},
		{"operator", "POST", "/api/v1/hosts/drain", 403},
		{"operator", "PUT", "/api/v1/pools/pool-1/image", 403},
		{"operator", "GET", "/api/v1/audit", 403},
		{"missing", "GET", "/api/v1/workers", 401},
	}
	for _, tc := range cases {
		t.Run(tc.token+"_"+tc.method+"_"+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.RemoteAddr = "10.0.0.20:1234"
			req.Header.Set("Authorization", "Bearer "+tc.token)
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("got %d want %d", res.Code, tc.want)
			}
		})
	}
}

func TestPairingEndpointsDoNotBypassOtherAPIAuth(t *testing.T) {
	m := New(Config{APIKeys: ParseKeys("key:admin:real-admin")})
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/v1/mcp/pair/status", 204},
		{"POST", "/api/v1/mcp/pair/request", 204},
		{"POST", "/api/v1/mcp/pair/status", 401},
		{"GET", "/api/v1/mcp/pair/request", 401},
		{"GET", "/api/v1/hosts", 401},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.RemoteAddr = "10.0.0.20:1234"
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Fatalf("%s %s: %d want %d", tc.method, tc.path, res.Code, tc.want)
		}
	}
}
