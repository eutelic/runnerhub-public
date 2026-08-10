package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/actions/scaleset"
)

func TestLoadConfigRepositoryScaleSet(t *testing.T) {
	key := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(key, []byte("test-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("RUNNERHUB_GITHUB_URL", "https://github.com/acme/app")
	t.Setenv("RUNNERHUB_SCALESET_NAME", "runnerhub")
	t.Setenv("RUNNERHUB_POOL_ID", "pool-docker")
	t.Setenv("RUNNERHUB_CONTROL_URL", "http://127.0.0.1:8080")
	t.Setenv("RUNNERHUB_CONTROL_TOKEN", "admin")
	t.Setenv("RUNNERHUB_GITHUB_APP_CLIENT_ID", "Iv1.test")
	t.Setenv("RUNNERHUB_GITHUB_APP_INSTALLATION_ID", "1234")
	t.Setenv("RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE", key)
	t.Setenv("RUNNERHUB_MIN_RUNNERS", "0")
	t.Setenv("RUNNERHUB_MAX_RUNNERS", "2")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RegistrationURL != "https://github.com/acme/app" {
		t.Fatalf("registration URL=%q", cfg.RegistrationURL)
	}
	if cfg.ControlURL != "http://127.0.0.1:8080" {
		t.Fatalf("control URL=%q", cfg.ControlURL)
	}
	if cfg.AppInstallationID != 1234 {
		t.Fatalf("installation id=%d", cfg.AppInstallationID)
	}
	if cfg.MinRunners != 0 || cfg.MaxRunners != 2 {
		t.Fatalf("min/max=%d/%d", cfg.MinRunners, cfg.MaxRunners)
	}
	if cfg.RunnerGroup != scaleset.DefaultRunnerGroup {
		t.Fatalf("runner group=%q", cfg.RunnerGroup)
	}
}

func TestLoadConfigRejectsUnprotectedRemoteControl(t *testing.T) {
	t.Setenv("RUNNERHUB_GITHUB_URL", "https://github.com/acme/app")
	t.Setenv("RUNNERHUB_SCALESET_NAME", "runnerhub")
	t.Setenv("RUNNERHUB_POOL_ID", "pool-docker")
	t.Setenv("RUNNERHUB_CONTROL_URL", "http://runnerhub.example.com:8080")

	if _, err := loadConfig(); err == nil {
		t.Fatal("remote plain HTTP control URL was accepted")
	}
}

func TestLoadConfigRejectsInvalidRunnerBounds(t *testing.T) {
	t.Setenv("RUNNERHUB_GITHUB_URL", "https://github.com/acme/app")
	t.Setenv("RUNNERHUB_SCALESET_NAME", "runnerhub")
	t.Setenv("RUNNERHUB_POOL_ID", "pool-docker")
	t.Setenv("RUNNERHUB_CONTROL_URL", "http://127.0.0.1:8080")
	t.Setenv("RUNNERHUB_MIN_RUNNERS", "3")
	t.Setenv("RUNNERHUB_MAX_RUNNERS", "2")

	if _, err := loadConfig(); err == nil {
		t.Fatal("min runners greater than max runners was accepted")
	}
}

func TestValidateExistingScaleSet(t *testing.T) {
	set := &scaleset.RunnerScaleSet{
		RunnerGroupID: 1,
		Labels:        []scaleset.Label{{Name: "linux"}, {Name: "runnerhub"}},
	}
	want := []scaleset.Label{{Name: "runnerhub"}, {Name: "linux"}}
	if err := validateExistingScaleSet(set, 1, want); err != nil {
		t.Fatalf("matching scale set rejected: %v", err)
	}
	if err := validateExistingScaleSet(set, 2, want); err == nil {
		t.Fatal("runner-group mismatch accepted")
	}
	if err := validateExistingScaleSet(set, 1, []scaleset.Label{{Name: "other"}}); err == nil {
		t.Fatal("label mismatch accepted")
	}
}

func TestHelpers(t *testing.T) {
	if !activeState("registering") || !activeState("busy") || activeState("failed") {
		t.Fatal("active worker state classification is incorrect")
	}
	if got := repoName("acme", "app"); got != "acme/app" {
		t.Fatalf("repoName=%q", got)
	}
	if err := requireProtectedURL("http://127.0.0.1:8080", false, "control"); err != nil {
		t.Fatalf("loopback HTTP rejected: %v", err)
	}
	if err := requireProtectedURL("http://runnerhub.example.com", false, "control"); err == nil {
		t.Fatal("remote HTTP accepted")
	}
}
