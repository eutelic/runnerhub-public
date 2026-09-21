package deployer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeAppInstaller struct {
	app     string
	compose string
}

func (f *fakeAppInstaller) EnsureCustomApp(_ context.Context, app, compose string) error {
	f.app = app
	f.compose = compose
	return nil
}

type failingAppInstaller struct{}

func (failingAppInstaller) EnsureCustomApp(context.Context, string, string) error {
	return errors.New("simulated TrueNAS install failure")
}

func TestBootstrapManagerInstallWritesPrivateConfigAndInstallsApp(t *testing.T) {
	root := t.TempDir()
	fake := &fakeAppInstaller{}
	manager := &BootstrapManager{
		Root: root,
		NewClient: func(TrueNASConfig) (TrueNASAppInstaller, error) {
			return fake, nil
		},
	}

	result, err := manager.Install(context.Background(), BootstrapRequest{
		Endpoint:     "wss://truenas.example/api/current",
		Username:     "runnerhub_deployer",
		APIKey:       "secret-api-key",
		HostDataPath: "/mnt/tank/runnerhub/data",
		RunnerHubApp: "runnerhub",
		HealthURL:    "http://truenas.example:8788/healthz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeployerApp != "runnerhub-deployer" || result.TargetID != "runnerhub" {
		t.Fatalf("unexpected bootstrap result: %#v", result)
	}
	if len(result.WorkflowToken) != 64 {
		t.Fatalf("expected 64-char workflow token, got %d", len(result.WorkflowToken))
	}
	if fake.app != "runnerhub-deployer" {
		t.Fatalf("unexpected installed app %q", fake.app)
	}
	if strings.Contains(fake.compose, "secret-api-key") {
		t.Fatal("generated Compose must not contain the TrueNAS API key")
	}
	for _, want := range []string{
		"/mnt/tank/runnerhub/data/deployer-bootstrap/data",
		"/mnt/tank/runnerhub/data/deployer-bootstrap/config/deployments.json",
		"/mnt/tank/runnerhub/data/deployer-bootstrap/secrets/truenas-api-key",
		"wss://truenas.example/api/current",
	} {
		if !strings.Contains(fake.compose, want) {
			t.Fatalf("generated Compose missing %q", want)
		}
	}

	keyBytes, err := os.ReadFile(filepath.Join(root, "secrets", "truenas-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	if string(keyBytes) != "secret-api-key\n" {
		t.Fatalf("unexpected API key file contents %q", string(keyBytes))
	}

	policyBytes, err := os.ReadFile(filepath.Join(root, "config", "deployments.json"))
	if err != nil {
		t.Fatal(err)
	}
	var policy Policy
	if err := json.Unmarshal(policyBytes, &policy); err != nil {
		t.Fatal(err)
	}
	if len(policy.Targets) != 1 {
		t.Fatalf("expected one deployment target, got %d", len(policy.Targets))
	}
	target := policy.Targets[0]
	sum := sha256.Sum256([]byte(result.WorkflowToken))
	if target.TokenSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("policy token digest does not match returned workflow token")
	}
	if target.App != "runnerhub" || target.Repository != "eutelic/runnerhub" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestBootstrapManagerRestoresExistingFilesWhenInstallFails(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"config", "secrets", "data"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	keyPath := filepath.Join(root, "secrets", "truenas-api-key")
	policyPath := filepath.Join(root, "config", "deployments.json")
	statePath := filepath.Join(root, "state.json")
	original := map[string]string{
		keyPath:    "old-key\n",
		policyPath: "{\"old\":true}\n",
		statePath:  "{\"old\":true}\n",
	}
	for path, value := range original {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	manager := &BootstrapManager{
		Root: root,
		NewClient: func(TrueNASConfig) (TrueNASAppInstaller, error) {
			return failingAppInstaller{}, nil
		},
	}
	_, err := manager.Install(context.Background(), BootstrapRequest{
		Endpoint:     "wss://truenas.example/api/current",
		Username:     "runnerhub_deployer",
		APIKey:       "new-secret-api-key",
		HostDataPath: "/mnt/tank/runnerhub/data",
		RunnerHubApp: "runnerhub",
		HealthURL:    "http://truenas.example:8788/healthz",
	})
	if err == nil {
		t.Fatal("expected install failure")
	}
	for path, want := range original {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != want {
			t.Fatalf("bootstrap rollback changed %s: got %q want %q", path, string(got), want)
		}
	}
}

func TestBootstrapManagerRejectsUnsafeHostDataPath(t *testing.T) {
	manager := &BootstrapManager{Root: t.TempDir()}
	_, err := manager.Install(context.Background(), BootstrapRequest{
		Endpoint:     "wss://truenas.example/api/current",
		Username:     "runnerhub_deployer",
		APIKey:       "secret-api-key",
		HostDataPath: "/mnt/tank/runnerhub/data/../secrets",
		RunnerHubApp: "runnerhub",
		HealthURL:    "http://truenas.example:8788/healthz",
	})
	if err == nil {
		t.Fatal("expected unsafe host data path to be rejected")
	}
}

func TestBootstrapStatusWithoutState(t *testing.T) {
	manager := &BootstrapManager{Root: t.TempDir()}
	status := manager.Status(context.Background())
	if !status.Available || status.Configured || status.Healthy {
		t.Fatalf("unexpected status: %#v", status)
	}
}
