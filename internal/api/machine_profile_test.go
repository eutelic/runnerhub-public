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


func TestUpdateHostPriority(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	host := core.Host{ID: "host-1", Name: "box", Provider: "docker", State: core.HostOnline}
	if err := st.UpsertHost(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Store: st,
		Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{
			"admin-secret": {Name: "test-admin", Role: auth.RoleAdmin},
		}}),
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/hosts/host-1", bytes.NewBufferString(`{"priority":100}`))
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
	if got.Priority != 100 {
		t.Fatalf("priority=%d, want 100", got.Priority)
	}
}

func TestHostPersistentProfileIsNotGeneralPurpose(t *testing.T) {
    for _,tt:=range []struct{name string;isolation core.IsolationMode;wantError bool}{
        {"runnerhub-android-emulator",core.IsolationHostPersistent,false},
        {"runnerhub-fast",core.IsolationHostPersistent,true},
        {"runnerhub",core.IsolationHostPersistent,true},
        {"runnerhub-fast",core.IsolationContainerTrusted,false},
        {"runnerhub-publisher",core.IsolationContainerDinD,false},
    } {
        err:=requireDedicatedNativeProfile(tt.name,tt.isolation)
        if (err!=nil)!=tt.wantError {
            t.Errorf("name=%q isolation=%q err=%v; wantError=%v",tt.name,tt.isolation,err,tt.wantError)
        }
    }
}
func TestProfileIsolationDefaultsAndAllowsDinD(t *testing.T) {
	got, err := profileIsolation("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != core.IsolationContainerTrusted {
		t.Fatalf("default isolation=%q, want %q", got, core.IsolationContainerTrusted)
	}

	got, err = profileIsolation(core.IsolationContainerDinD, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != core.IsolationContainerDinD {
		t.Fatalf("DinD isolation=%q", got)
	}

	existing := &core.RunnerPool{Isolation: core.IsolationContainerDinD}
	got, err = profileIsolation("", existing)
	if err != nil {
		t.Fatal(err)
	}
	if got != core.IsolationContainerDinD {
		t.Fatalf("existing isolation=%q, want preserved DinD", got)
	}

	got, err = profileIsolation(core.IsolationHostPersistent, nil)
	if err != nil || got != core.IsolationHostPersistent {
		t.Fatalf("native process isolation=%q err=%v", got, err)
	}
	if _, err := profileIsolation(core.IsolationMicroVM, nil); err == nil {
		t.Fatal("unsupported custom profile isolation should fail")
	}
}
