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
	req machinebootstrap.Request
}

func (c *captureMachineBootstrap) Bootstrap(_ context.Context, req machinebootstrap.Request, _ string, _ machinebootstrap.WaitForHost) (core.Host, error) {
	c.req = req
	return core.Host{ID: "host-new", Name: req.HostName, Provider: "docker", State: core.HostOnline}, nil
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
