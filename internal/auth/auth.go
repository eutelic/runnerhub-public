package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strings"
)

type Role int

const (
	RoleNone Role = iota
	RoleViewer
	RoleOperator
	RoleAdmin
	// RoleScaler is intentionally outside the viewer/operator/admin ordering.
	// Middleware handles it through an explicit endpoint allowlist.
	RoleScaler
)

type Principal struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
	MCP  bool   `json:"-"`
}

type SessionAuthenticator func(*http.Request) (Principal, bool)

type Config struct {
	APIKeys              map[string]Principal
	TrustOIDCHeaders     bool
	SessionAuthenticator SessionAuthenticator
	PairedAuthenticator  func(string) (Principal, bool)
}

type Manager struct {
	keys       map[[32]byte]Principal
	trustOIDC  bool
	browser    SessionAuthenticator
	paired     func(string) (Principal, bool)
	localAdmin bool
}

type ctxKey struct{}

func New(c Config) *Manager {
	m := &Manager{
		keys:       map[[32]byte]Principal{},
		trustOIDC:  c.TrustOIDCHeaders,
		browser:    c.SessionAuthenticator,
		paired:     c.PairedAuthenticator,
		localAdmin: len(c.APIKeys) == 0 && !c.TrustOIDCHeaders && c.SessionAuthenticator == nil,
	}
	for k, p := range c.APIKeys {
		if strings.TrimSpace(k) == "" || p.Role == RoleNone {
			continue
		}
		m.keys[sha256.Sum256([]byte(k))] = p
	}
	return m
}

func (m *Manager) Authenticate(r *http.Request) (Principal, error) {
	if m == nil {
		return Principal{}, errors.New("authentication is not configured")
	}
	if m.localAdmin && remoteIsLoopback(r.RemoteAddr) {
		return Principal{Name: "local", Role: RoleAdmin}, nil
	}
	if m.trustOIDC && remoteIsLoopback(r.RemoteAddr) {
		if u := strings.TrimSpace(r.Header.Get("X-RunnerHub-User")); u != "" {
			role := parseRole(r.Header.Get("X-RunnerHub-Role"))
			if role == RoleNone {
				role = RoleViewer
			}
			return Principal{Name: u, Role: role}, nil
		}
	}
	if m.browser != nil {
		if p, ok := m.browser(r); ok && p.Role != RoleNone {
			return p, nil
		}
	}

	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	token := ""
	if strings.HasPrefix(authz, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	}
	if token != "" && m.paired != nil {
		if p, ok := m.paired(token); ok {
			return p, nil
		}
	}
	sum := sha256.Sum256([]byte(token))
	for key, p := range m.keys {
		if subtle.ConstantTimeCompare(sum[:], key[:]) == 1 {
			return p, nil
		}
	}
	return Principal{}, errors.New("invalid api credential")
}

func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path == "/api/v1/mcp/pair/status" && r.Method == http.MethodGet) ||
			(r.URL.Path == "/api/v1/mcp/pair/request" && r.Method == http.MethodPost) ||
			r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/api/v1/agent/") || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		p, err := m.Authenticate(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if p.Role == RoleScaler {
			if !scalerAllowed(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
			return
		}

		if p.MCP && !mcpAllowed(r, p.Role) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		need := RoleViewer
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			need = RoleOperator
		}
		if p.Role < need {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func scalerAllowed(r *http.Request) bool {
	path := r.URL.Path
	if r.Method == http.MethodGet {
		if path == "/api/v1/pools" || path == "/api/v1/workers" || path == "/api/v1/scalesets/configs" {
			return true
		}
		const prefix = "/api/v1/pools/"
		const healthSuffix = "/health"
		if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, healthSuffix) {
			poolID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), healthSuffix)
			return poolID != "" && !strings.Contains(poolID, "/")
		}
		return false
	}
	if r.Method != http.MethodPost {
		return false
	}
	if path == "/api/v1/scale-events" || path == "/api/v1/scalesets/status" {
		return true
	}
	const prefix = "/api/v1/pools/"
	const suffix = "/scale-workers"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	poolID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return poolID != "" && !strings.Contains(poolID, "/")
}

func PrincipalFrom(ctx context.Context) Principal {
	if p, ok := ctx.Value(ctxKey{}).(Principal); ok {
		return p
	}
	return Principal{Name: "system", Role: RoleNone}
}

func parseRole(v string) Role {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "admin":
		return RoleAdmin
	case "operator":
		return RoleOperator
	case "viewer":
		return RoleViewer
	case "scaler":
		return RoleScaler
	default:
		return RoleNone
	}
}

func ParseKeys(spec string) map[string]Principal {
	out := map[string]Principal{}
	for _, part := range strings.Split(spec, ",") {
		fields := strings.SplitN(part, ":", 3)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSpace(fields[0])
		role := parseRole(fields[1])
		if key == "" || role == RoleNone {
			continue
		}
		name := "api"
		if len(fields) == 3 && strings.TrimSpace(fields[2]) != "" {
			name = strings.TrimSpace(fields[2])
		}
		out[key] = Principal{Name: name, Role: role}
	}
	return out
}

func remoteIsLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// mcpAllowed prevents a paired operator credential from becoming a general API
// operator credential. This is enforced server-side before route dispatch.
func mcpAllowed(r *http.Request, role Role) bool {
	path := r.URL.Path
	if r.Method == http.MethodGet {
		switch path {
		case "/api/v1/runtime-health", "/api/v1/pools", "/api/v1/hosts",
			"/api/v1/workers", "/api/v1/workers/history",
			"/api/v1/workers/progress", "/api/v1/scalesets/status",
			"/api/v1/fleet/versions":
			return true
		}
		if strings.HasPrefix(path, "/api/v1/pools/") &&
			strings.HasSuffix(path, "/health") {
			parts := strings.Split(path, "/")
			return len(parts) == 6 && parts[4] != "" && parts[5] == "health"
		}
		return false
	}
	if r.Method == http.MethodPost && role == RoleOperator {
		parts := strings.Split(path, "/")
		return len(parts) == 6 && parts[1] == "api" && parts[2] == "v1" &&
			parts[3] == "pools" && parts[4] != "" && parts[5] == "workers"
	}
	return false
}
