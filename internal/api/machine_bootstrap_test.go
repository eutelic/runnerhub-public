package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eutelic/runnerhub/internal/agenthub"
	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/machinebootstrap"
	"github.com/eutelic/runnerhub/internal/store"
)

type captureMachineBootstrap struct {
	req           machinebootstrap.Request
	noVerifiedKVM bool
}

func (c *captureMachineBootstrap) Bootstrap(_ context.Context, req machinebootstrap.Request, _ string, _ machinebootstrap.WaitForHost) (core.Host, error) {
	c.req = req
	provider := "docker"
	tags := map[string]string{}
	if req.AndroidEmulator {
		provider = "process"
		if !c.noVerifiedKVM {
			tags["hardware.kvm"] = "true"
		}
	}
	return core.Host{ID: "host-new", Name: req.HostName, Provider: provider, State: core.HostOnline, SystemTags: tags}, nil
}

func (c *captureMachineBootstrap) ScanHostKey(_ context.Context, _ string) ([]string, error) {
	return []string{"SHA256:observed-test-key"}, nil
}

func TestBootstrapHostPassesEphemeralCredentialsAndPublisherCapability(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	hub, err := agenthub.New(st, "0123456789abcdef", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	capture := &captureMachineBootstrap{}
	svc := &Service{
		Store:               st,
		Hub:                 hub,
		MachineBootstrap:    capture,
		MachineBootstrapURL: "http://runnerhub.example:8788",
		MachineAgentImage:   "ghcr.io/eutelic/runnerhub-agent:latest",
		Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{
			"admin-secret": {Name: "test-admin", Role: auth.RoleAdmin},
		}}),
	}

	body := []byte(`{
		"address":"TRUENAS_HOST",
		"user":"compute",
		"password":"ssh-and-sudo-password",
		"hostKeyFingerprint":"SHA256:trusted-host-key",
		"name":"compute",
		"tags":["fast"],
		"priority":100,
		"registryUsername":"example-user",
		"registryPassword":"registry-token",
		"installDocker":true,
		"allowPrivilegedWorkers":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/bootstrap", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if capture.req.SudoPassword != "ssh-and-sudo-password" {
		t.Fatalf("sudo password fallback=%q", capture.req.SudoPassword)
	}
	if capture.req.HostKeyFingerprint != "SHA256:trusted-host-key" {
		t.Fatalf("host fingerprint=%q", capture.req.HostKeyFingerprint)
	}
	if capture.req.RegistryUsername != "example-user" || capture.req.RegistryPassword != "registry-token" {
		t.Fatalf("registry credentials not forwarded: user=%q password=%q", capture.req.RegistryUsername, capture.req.RegistryPassword)
	}
	if !capture.req.AllowPrivilegedWorkers {
		t.Fatal("publisher capability was not forwarded")
	}
	if !capture.req.InstallDocker {
		t.Fatal("installDocker was not forwarded")
	}
	stored, err := st.GetHost(context.Background(), "host-new")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Priority != 100 {
		t.Fatalf("stored priority=%d, want 100", stored.Priority)
	}
}

func TestBootstrapHostPrefersExplicitSudoPassword(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	hub, err := agenthub.New(st, "0123456789abcdef", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	capture := &captureMachineBootstrap{}
	svc := &Service{
		Store:               st,
		Hub:                 hub,
		MachineBootstrap:    capture,
		MachineBootstrapURL: "https://runnerhub.example",
		MachineAgentImage:   "ghcr.io/eutelic/runnerhub-agent:latest",
		Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{
			"admin-secret": {Name: "test-admin", Role: auth.RoleAdmin},
		}}),
	}

	body := []byte(`{
		"address":"compute.example",
		"user":"compute",
		"password":"ssh-password",
		"sudoPassword":"different-sudo-password",
		"hostKeyFingerprint":"SHA256:trusted-host-key",
		"name":"compute"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/bootstrap", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if capture.req.SudoPassword != "different-sudo-password" {
		t.Fatalf("sudo password=%q, want explicit value", capture.req.SudoPassword)
	}
}

func TestScanSSHHostKeyRequiresAdminAndReturnsFingerprints(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	capture := &captureMachineBootstrap{}
	svc := &Service{
		Store:            st,
		MachineBootstrap: capture,
		Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{
			"admin-secret": {Name: "test-admin", Role: auth.RoleAdmin},
		}}),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/ssh-fingerprints", bytes.NewBufferString(`{"address":"compute.example"}`))
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("SHA256:observed-test-key")) {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestBootstrapHostRejectsMissingFingerprint(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub, err := agenthub.New(st, "0123456789abcdef", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Store: st, Hub: hub, MachineBootstrap: &captureMachineBootstrap{},
		MachineBootstrapURL: "https://runnerhub.example",
		Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{
			"admin-secret": {Name: "test-admin", Role: auth.RoleAdmin},
		}}),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/bootstrap", bytes.NewBufferString(`{"address":"compute.example","user":"compute","name":"compute"}`))
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("fingerprint is required")) {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestBootstrapAndroidModeAddsVerifiedKVMProfilePlacement(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub, err := agenthub.New(st, "0123456789abcdef", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	capture := &captureMachineBootstrap{}
	svc := &Service{Store: st, Hub: hub, MachineBootstrap: capture, MachineBootstrapURL: "http://runnerhub.example:8788",
		MachineAgentImage: "ghcr.io/eutelic/runnerhub-agent:latest",
		Auth:              auth.New(auth.Config{APIKeys: map[string]auth.Principal{"admin-secret": {Name: "admin", Role: auth.RoleAdmin}}})}
	body := []byte(`{"address":"TRUENAS_HOST","user":"compute","hostKeyFingerprint":"SHA256:trusted","name":"compute-android","mode":"android-emulator","tags":["fast"],"priority":1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/bootstrap", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !capture.req.AndroidEmulator {
		t.Fatal("dedicated native installer not selected")
	}
	if capture.req.AllowPrivilegedWorkers {
		t.Fatal("native installer must not enable privileged workers")
	}
	// Verification is system-derived; only the placement label is operator-defined.
	if _, err := st.GetHost(context.Background(), "host-new"); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapAndroidRejectsPrivilegedAndSystemTagSpoof(t *testing.T) {
	for _, body := range []string{
		`{"address":"TRUENAS_HOST","user":"compute","hostKeyFingerprint":"SHA256:trusted","name":"compute-android","mode":"android-emulator","allowPrivilegedWorkers":true}`,
		`{"address":"TRUENAS_HOST","user":"compute","hostKeyFingerprint":"SHA256:trusted","name":"compute-android","mode":"android-emulator","tags":["hardware.kvm"]}`,
	} {
		st, err := store.OpenSQLite(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		hub, err := agenthub.New(st, "0123456789abcdef", "0123456789abcdef0123456789abcdef")
		if err != nil {
			t.Fatal(err)
		}
		svc := &Service{Store: st, Hub: hub, MachineBootstrap: &captureMachineBootstrap{}, MachineBootstrapURL: "https://runnerhub.example",
			Auth: auth.New(auth.Config{APIKeys: map[string]auth.Principal{"admin-secret": {Name: "admin", Role: auth.RoleAdmin}}})}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/bootstrap", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer admin-secret")
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body=%s returned %d, expected 400: %s", body, rr.Code, rr.Body.String())
		}
		st.Close()
	}
}

func TestBootstrapAndroidRejectsAbsentVerifiedKVM(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub, err := agenthub.New(st, "0123456789abcdef", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st, Hub: hub, MachineBootstrap: &captureMachineBootstrap{noVerifiedKVM: true},
		MachineBootstrapURL: "http://runnerhub.example:8788",
		Auth:                auth.New(auth.Config{APIKeys: map[string]auth.Principal{"admin-secret": {Name: "admin", Role: auth.RoleAdmin}}})}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/bootstrap", bytes.NewBufferString(`{"address":"TRUENAS_HOST","user":"compute","hostKeyFingerprint":"SHA256:trusted","name":"compute-android","mode":"android-emulator"}`))
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("unverified KVM incorrectly accepted: %d: %s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("hardware.kvm")) {
		t.Fatalf("missing actionable KVM error: %s", rr.Body.String())
	}
}
