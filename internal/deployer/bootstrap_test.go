package deployer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

func (f *fakeAppInstaller) FindAppMount(context.Context, string, string) (string, error) {
	return "/mnt/tank/runnerhub/data", nil
}

type pendingAppInstaller struct{}

func (pendingAppInstaller) EnsureCustomApp(context.Context, string, string) error {
	return fmt.Errorf("%w: TrueNAS is still creating the app", ErrAppInstallMayBeInProgress)
}

func (pendingAppInstaller) FindAppMount(context.Context, string, string) (string, error) {
	return "/mnt/tank/runnerhub/data", nil
}

type failingAppInstaller struct{}

func (failingAppInstaller) EnsureCustomApp(context.Context, string, string) error {
	return errors.New("simulated TrueNAS install failure")
}

func (failingAppInstaller) FindAppMount(context.Context, string, string) (string, error) {
	return "/mnt/tank/runnerhub/data", nil
}

func TestBootstrapManagerInstallWritesPrivateConfigAndInstallsApp(t *testing.T) {
	root := t.TempDir()
	fake := &fakeAppInstaller{}
	manager := &BootstrapManager{
		Root: root,
		MaintenanceToken: "test-admin-key",
		NewClient: func(TrueNASConfig) (TrueNASBootstrapClient, error) {
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
	if !strings.Contains(fake.compose, `published: "8790"`) {
		t.Fatal("default deployment port 8790 missing from generated Compose")
	}
	if fake.app != "runnerhub-deployer" {
		t.Fatalf("unexpected installed app %q", fake.app)
	}
	if strings.Contains(fake.compose, "secret-api-key") || strings.Contains(fake.compose,"test-admin-key") {
		t.Fatal("generated Compose must not contain the TrueNAS API key")
	}
	for _, want := range []string{
		"/mnt/tank/runnerhub/data/deployer-bootstrap/data",
		"/mnt/tank/runnerhub/data/deployer-bootstrap/config/deployments.json",
		"/mnt/tank/runnerhub/data/deployer-bootstrap/secrets/truenas-api-key",
		"/mnt/tank/runnerhub/data/deployer-bootstrap/secrets/runnerhub-maintenance-key",
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
	if !target.SelfRedeploy||target.MaintenanceURL!="http://truenas.example:8788"{
		t.Fatalf("RunnerHub maintenance policy not configured: %+v",target)
	}
	key,err:=os.ReadFile(filepath.Join(root,"secrets","runnerhub-maintenance-key"))
	if err!=nil||string(key)!="test-admin-key\n"{t.Fatalf("maintenance credential missing or invalid: %v",err)}
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
	maintenanceKeyPath:=filepath.Join(root,"secrets","runnerhub-maintenance-key")
	policyPath := filepath.Join(root, "config", "deployments.json")
	statePath := filepath.Join(root, "state.json")
	original := map[string]string{
		keyPath:    "old-key\n",
		maintenanceKeyPath:"old-admin-key\n",
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
		MaintenanceToken: "test-admin-key",
		NewClient: func(TrueNASConfig) (TrueNASBootstrapClient, error) {
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

func TestBootstrapManagerAutoDetectsHostDataPath(t *testing.T) {
	root := t.TempDir()
	fake := &fakeAppInstaller{}
	manager := &BootstrapManager{
		Root: root,
		MaintenanceToken: "test-admin-key",
		NewClient: func(TrueNASConfig) (TrueNASBootstrapClient, error) {
			return fake, nil
		},
	}
	_, err := manager.Install(context.Background(), BootstrapRequest{
		Endpoint:     "wss://truenas.example/api/current",
		Username:     "runnerhub_deployer",
		APIKey:       "secret-api-key",
		RunnerHubApp: "runnerhub",
		HealthURL:    "http://truenas.example:8788/healthz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.compose, "/mnt/tank/runnerhub/data/deployer-bootstrap/data") {
		t.Fatalf("auto-detected data path missing from Compose: %s", fake.compose)
	}
}

func TestBootstrapManagerRejectsUnsafeHostDataPath(t *testing.T) {
	manager := &BootstrapManager{Root: t.TempDir(),MaintenanceToken:"test-admin-key"}
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

func TestBootstrapManagerCustomPort(t *testing.T) {
	root := t.TempDir()
	fake := &fakeAppInstaller{}
	manager := &BootstrapManager{
		Root: root,
		MaintenanceToken: "test-admin-key",
		NewClient: func(TrueNASConfig) (TrueNASBootstrapClient, error) { return fake, nil },
	}
	result, err := manager.Install(context.Background(), BootstrapRequest{
		Endpoint:     "wss://truenas.example:8423/api/current",
		Port:         8790,
		Username:     "runnerhub_deployer",
		APIKey:       "secret-api-key",
		HostDataPath: "/mnt/tank/runnerhub/data",
		RunnerHubApp: "runnerhub",
		HealthURL:    "http://truenas.example:8788/healthz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.compose, `published: "8790"`) {
		t.Fatal("custom port not propagated to compose")
	}
	if result.DeployerURL != "http://truenas.example:8790" {
		t.Fatalf("unexpected deployer URL %q", result.DeployerURL)
	}
}

func TestBootstrapManagerRejectsInvalidDeployerPortsBeforeWritingSecrets(t *testing.T) {
	for _, port := range []int{-1, 1, 1023, 65536} {
		t.Run(fmt.Sprintf("port_%d", port), func(t *testing.T) {
			root := t.TempDir()
			manager := &BootstrapManager{Root: root}
			_, err := manager.Install(context.Background(), BootstrapRequest{
				Endpoint: "wss://truenas.example/api/current",
				Port: port,
				Username: "runnerhub_deployer",
				APIKey: "test-api-key",
				HostDataPath: "/mnt/tank/runnerhub/data",
				RunnerHubApp: "runnerhub",
				HealthURL: "http://truenas.example:8788/healthz",
			})
			if err == nil || !strings.Contains(err.Error(), "redeployer port") {
				t.Fatalf("expected port validation error, got %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "secrets", "truenas-api-key")); !os.IsNotExist(statErr) {
				t.Fatalf("invalid port must not create a secret file: %v", statErr)
			}
		})
	}
}

func TestBootstrapManagerRetainsSecretWhenTrueNASInstallStillPending(t *testing.T) {
	root := t.TempDir()
	manager := &BootstrapManager{
		Root: root,
		MaintenanceToken: "test-admin-key",
		NewClient: func(TrueNASConfig) (TrueNASBootstrapClient, error) {
			return pendingAppInstaller{}, nil
		},
	}
	_, err := manager.Install(context.Background(), BootstrapRequest{
		Endpoint:     "wss://truenas.example/api/current",
		Username:     "runnerhub_deployer",
		APIKey:       "test-api-key",
		HostDataPath: "/mnt/tank/runnerhub/data",
		RunnerHubApp: "runnerhub",
		HealthURL:    "http://truenas.example:8788/healthz",
	})
	if !errors.Is(err, ErrAppInstallMayBeInProgress) {
		t.Fatalf("expected pending-install error, got %v", err)
	}
	for _, path := range []string{"secrets/truenas-api-key", "config/deployments.json", "state.json"} {
		if _, statErr := os.Stat(filepath.Join(root, path)); statErr != nil {
			t.Fatalf("lost %s while TrueNAS may still mount it: %v", path, statErr)
		}
	}
	key, err := os.ReadFile(filepath.Join(root, "secrets", "truenas-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != "test-api-key\n" {
		t.Fatal("pending installation did not retain the correct API key")
	}
}


type portConflictInstaller struct{}

func (portConflictInstaller) EnsureCustomApp(context.Context, string, string) error {
	return fmt.Errorf("%w: host bind failed", ErrAppPortConflict)
}
func (portConflictInstaller) FindAppMount(context.Context, string, string) (string, error) {
	return "/mnt/tank/runnerhub/data", nil
}

func TestBootstrapPortConflictIsActionableAndRollsBackSecrets(t *testing.T) {
	root := t.TempDir()
	manager := &BootstrapManager{
		Root: root,
		MaintenanceToken: "admin-key",
		NewClient: func(TrueNASConfig) (TrueNASBootstrapClient, error) {
			return portConflictInstaller{}, nil
		},
	}
	_, err := manager.Install(context.Background(), BootstrapRequest{
		Endpoint: "wss://truenas.example/api/current",
		Port: 8790,
		Username: "deployer",
		APIKey: "new-secret",
		HostDataPath: "/mnt/tank/runnerhub/data",
		RunnerHubApp: "runnerhub",
		HealthURL: "http://truenas.example:8788/healthz",
	})
	if !errors.Is(err, ErrAppPortConflict) ||
		!strings.Contains(err.Error(), "redeployer port 8790") ||
		!strings.Contains(err.Error(), "choose a different port") {
		t.Fatalf("expected actionable port conflict: %v", err)
	}
	for _, path := range []string{
		"state.json", "config/deployments.json", "secrets/truenas-api-key",
		"secrets/runnerhub-maintenance-key",
	} {
		if _, statErr := os.Stat(filepath.Join(root, path)); !os.IsNotExist(statErr) {
			t.Fatalf("failed install must not retain changed %s: %v", path, statErr)
		}
	}
}
