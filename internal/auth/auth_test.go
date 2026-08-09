package auth

import (
	"net/http/httptest"
	"testing"
)

func TestAPIKeyRoles(t *testing.T) {
	m := New(Config{APIKeys: map[string]Principal{"secret": {Name: "ops", Role: RoleOperator}}})
	r := httptest.NewRequest("GET", "/api/v1/hosts", nil)
	r.Header.Set("Authorization", "Bearer secret")
	p, err := m.Authenticate(r)
	if err != nil || p.Name != "ops" || p.Role != RoleOperator {
		t.Fatalf("unexpected principal %#v %v", p, err)
	}
}
func TestRejectsBadKey(t *testing.T) {
	m := New(Config{APIKeys: map[string]Principal{"secret": {Role: RoleAdmin}}})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer nope")
	if _, err := m.Authenticate(r); err == nil {
		t.Fatal("expected rejection")
	}
}
