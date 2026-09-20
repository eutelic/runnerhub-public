package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestProfileNameNormalizesShortName(t *testing.T) {
	got, err := profileName("fast")
	if err != nil {
		t.Fatal(err)
	}
	if got != "runnerhub-fast" {
		t.Fatalf("profileName=%q, want runnerhub-fast", got)
	}
	if _, err := profileName("publisher"); err == nil {
		t.Fatal("publisher alias should resolve to reserved runnerhub-publisher and be rejected")
	}
}

func TestUpdateHostTagsPreservesSystemCapabilities(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	host := core.Host{
		ID:         "host-1",
		Name:       "box",
		Provider:   "docker",
		State:      core.HostOnline,
		SystemTags: map[string]string{"isolation.container-trusted": "true"},
		Tags:       map[string]string{"isolation.container-trusted": "true"},
	}
	if err := st.UpsertHost(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Store: st,
		Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{
			"admin-secret": {Name: "test-admin", Role: auth.RoleAdmin},
		}}),
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/hosts/host-1", bytes.NewBufferString(`{"tags":{"fast":"true"}}`))
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got, err := st.GetHost(context.Background(), "host-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tags["fast"] != "true" || got.Tags["isolation.container-trusted"] != "true" {
		t.Fatalf("effective tags=%v", got.Tags)
	}
	if got.UserTags["fast"] != "true" {
		t.Fatalf("user tags=%v", got.UserTags)
	}
}
