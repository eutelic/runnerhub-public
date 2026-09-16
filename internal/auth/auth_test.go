package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNoKeysOnlyAllowsLoopbackAdmin(t *testing.T) {
	m := New(Config{})
	local := &http.Request{RemoteAddr: "127.0.0.1:1234"}
	p, err := m.Authenticate(local)
	if err != nil || p.Role != RoleAdmin {
		t.Fatalf("local auth=%+v err=%v", p, err)
	}
	remote := &http.Request{RemoteAddr: "10.0.0.5:1234"}
	if _, err := m.Authenticate(remote); err == nil {
		t.Fatal("unauthenticated remote API access was allowed")
	}
}

func TestOIDCHeadersRequireLoopbackProxy(t *testing.T) {
	m := New(Config{TrustOIDCHeaders: true})
	r := &http.Request{RemoteAddr: "10.0.0.5:1234", Header: make(http.Header)}
	r.Header.Set("X-RunnerHub-User", "attacker")
	r.Header.Set("X-RunnerHub-Role", "admin")
	if _, err := m.Authenticate(r); err == nil {
		t.Fatal("trusted headers from remote client were accepted")
	}
	r.RemoteAddr = "127.0.0.1:1234"
	p, err := m.Authenticate(r)
	if err != nil || p.Name != "attacker" || p.Role != RoleAdmin {
		t.Fatalf("loopback proxy auth=%+v err=%v", p, err)
	}
}

func TestParseKeysDropsInvalidRolesAndAcceptsScaler(t *testing.T) {
	got := ParseKeys("good:operator:ops,scale:scaler:listener,bad:not-a-role:nope")
	if len(got) != 2 || got["good"].Role != RoleOperator || got["scale"].Role != RoleScaler {
		t.Fatalf("unexpected keys: %#v", got)
	}
}

func TestScalerRoleIsRestrictedToScaleSetEndpoints(t *testing.T) {
	m := New(Config{APIKeys: ParseKeys("scale-secret:scaler:listener")})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := m.Middleware(next)

	allowed := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/pools"},
		{http.MethodGet, "/api/v1/workers"},
		{http.MethodGet, "/api/v1/scalesets/configs"},
		{http.MethodGet, "/api/v1/pools/pool-docker/health"},
		{http.MethodPost, "/api/v1/scalesets/status"},
		{http.MethodPost, "/api/v1/pools/pool-docker/scale-workers"},
		{http.MethodPost, "/api/v1/scale-events"},
	}
	for _, tc := range allowed {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer scale-secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("allowed %s %s returned %d", tc.method, tc.path, w.Code)
		}
	}

	denied := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/audit"},
		{http.MethodGet, "/api/v1/hosts"},
		{http.MethodPost, "/api/v1/pools"},
		{http.MethodPost, "/api/v1/hosts/drain"},
		{http.MethodDelete, "/api/v1/cache/example"},
		{http.MethodPost, "/api/v1/pools/pool-docker/workers"},
	}
	for _, tc := range denied {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer scale-secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("denied %s %s returned %d", tc.method, tc.path, w.Code)
		}
	}
}

func TestBrowserSessionAuthenticatesRemoteAPI(t *testing.T) {
	m := New(Config{SessionAuthenticator: func(r *http.Request) (Principal, bool) {
		if r.Header.Get("X-Test-Session") != "ok" {
			return Principal{}, false
		}
		return Principal{Name: "github:example-user", Role: RoleAdmin}, true
	}})
	r := httptest.NewRequest(http.MethodGet, "http://runnerhub/api/v1/hosts", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Set("X-Test-Session", "ok")
	p, err := m.Authenticate(r)
	if err != nil || p.Name != "github:example-user" || p.Role != RoleAdmin {
		t.Fatalf("browser auth=%+v err=%v", p, err)
	}
}
