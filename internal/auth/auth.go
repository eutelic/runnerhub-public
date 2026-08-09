package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

type Role int

const (
	RoleNone Role = iota
	RoleViewer
	RoleOperator
	RoleAdmin
)

type Principal struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
}
type Config struct {
	APIKeys          map[string]Principal
	TrustOIDCHeaders bool
}
type Manager struct {
	keys      map[[32]byte]Principal
	trustOIDC bool
}
type ctxKey struct{}

func New(c Config) *Manager {
	if len(c.APIKeys) == 0 && !c.TrustOIDCHeaders {
		return nil
	}
	m := &Manager{keys: map[[32]byte]Principal{}, trustOIDC: c.TrustOIDCHeaders}
	for k, p := range c.APIKeys {
		m.keys[sha256.Sum256([]byte(k))] = p
	}
	return m
}
func (m *Manager) Authenticate(r *http.Request) (Principal, error) {
	if m == nil {
		return Principal{Name: "local", Role: RoleAdmin}, nil
	}
	if m.trustOIDC {
		if u := strings.TrimSpace(r.Header.Get("X-RunnerHub-User")); u != "" {
			role := parseRole(r.Header.Get("X-RunnerHub-Role"))
			if role == RoleNone {
				role = RoleViewer
			}
			return Principal{Name: u, Role: role}, nil
		}
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
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
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/api/v1/agent/") || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		p, err := m.Authenticate(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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
	}
	return RoleNone
}
func ParseKeys(spec string) map[string]Principal {
	out := map[string]Principal{}
	for _, part := range strings.Split(spec, ",") {
		fields := strings.SplitN(part, ":", 3)
		if len(fields) < 2 {
			continue
		}
		role := parseRole(fields[1])
		name := "api"
		if len(fields) == 3 && fields[2] != "" {
			name = fields[2]
		}
		out[fields[0]] = Principal{Name: name, Role: role}
	}
	return out
}
