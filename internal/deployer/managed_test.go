package deployer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeManagedTrueNAS struct {
	fakeTrueNAS
	inventory []TrueNASApp
}

func (f *fakeManagedTrueNAS) ListApps(context.Context) ([]TrueNASApp, error) {
	return f.inventory, nil
}

// A fake custom app implements the same pinned endpoint as the TrueNAS
// client while recording the exact immutable image visible in app.query.
func (f *fakeManagedTrueNAS) RedeployPinnedAndWait(ctx context.Context, app, image, previousDigest, targetDigest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	previousPinned := strings.TrimSuffix(strings.ToLower(image), ":latest") + "@" + previousDigest
	pinned := strings.TrimSuffix(strings.ToLower(image), ":latest") + "@" + targetDigest
	for i := range f.inventory {
		if f.inventory[i].Name != app {
			continue
		}
		for j, installed := range f.inventory[i].Images {
			if strings.EqualFold(installed, image) || strings.EqualFold(installed, previousPinned) {
				f.inventory[i].Images[j] = pinned
				f.apps = append(f.apps, app)
				return nil
			}
		}
	}
	return errors.New("pinned fake app image does not match previous release")
}

func newManagedService(t *testing.T, fake *fakeManagedTrueNAS, root string) *Service {
	t.Helper()
	return &Service{
		Policy:          testPolicy(t, "runnerhub-workflow-secret", ""),
		TrueNAS:         fake,
		AuditPath:       filepath.Join(root, "deployments.jsonl"),
		ManagementKey:   []byte("private-truenas-key"),
		ManagedAppsPath: filepath.Join(root, "managed-apps.json"),
	}
}

func signedManagedRequest(t *testing.T, key []byte, method, path, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	SignManagementRequest(req, key, []byte(body))
	return req
}

func testManagedInventory() *fakeManagedTrueNAS {
	return &fakeManagedTrueNAS{inventory: []TrueNASApp{
		{Name: "runnerhub-deployer", State: "RUNNING"},
		{Name: "sample-app", State: "RUNNING"},
		{Name: "file-ingest", State: "RUNNING"},
		{Name: "jellyfin", State: "RUNNING"},
	}}
}

func TestManagedAppTogglePersistenceAndRedeploy(t *testing.T) {
	root := t.TempDir()
	fake := testManagedInventory()
	svc := newManagedService(t, fake, root)
	handler := svc.Handler()
	key := svc.ManagementKey

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, signedManagedRequest(t, key, "GET", managementPrefix, ""))
	if list.Code != 200 {
		t.Fatalf("inventory code=%d body=%s", list.Code, list.Body.String())
	}
	var initial struct {
		Apps []ManagedApp `json:"apps"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if len(initial.Apps) != 4 {
		t.Fatalf("discovered apps: %+v", initial.Apps)
	}
	for _, app := range initial.Apps {
		if app.Name == "sample-app" && !app.Protected {
			t.Fatal("existing workflow target must be protected")
		}
		if app.Name == "runnerhub-deployer" && !app.Protected {
			t.Fatal("deployer must not be self-managed")
		}
	}
	enablePath := managementPrefix + "/file-ingest/enabled"
	enable := httptest.NewRecorder()
	handler.ServeHTTP(enable, signedManagedRequest(t, key, "POST", enablePath, `{"enabled":true}`))
	if enable.Code != 200 {
		t.Fatalf("enable code=%d body=%s", enable.Code, enable.Body.String())
	}

	mode, err := os.Stat(svc.ManagedAppsPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm() != 0600 {
		t.Fatalf("settings mode=%o, want 0600", mode.Mode().Perm())
	}

	restarted := newManagedService(t, fake, root)
	loaded, err := restarted.managedCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, app := range loaded {
		if app.Name == "file-ingest" {
			found = app.Enabled
		}
	}
	if !found {
		t.Fatal("enabled app setting not preserved across service restart")
	}
	redeploy := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(redeploy, signedManagedRequest(t, key, "POST", managementPrefix+"/file-ingest/redeploy", ""))
	if redeploy.Code != 200 {
		t.Fatalf("redeploy code=%d body=%s", redeploy.Code, redeploy.Body.String())
	}
	fake.mu.Lock()
	if len(fake.apps) != 1 || fake.apps[0] != "file-ingest" {
		t.Fatalf("redeploy targets=%v", fake.apps)
	}
	fake.mu.Unlock()

	disable := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(disable, signedManagedRequest(t, key, "POST", enablePath, `{"enabled":false}`))
	if disable.Code != 200 {
		t.Fatalf("disable code=%d body=%s", disable.Code, disable.Body.String())
	}
	denied := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(denied, signedManagedRequest(t, key, "POST", managementPrefix+"/file-ingest/redeploy", ""))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("disabled app redeploy status=%d, want 403", denied.Code)
	}
}

func TestManagedAppsRejectInvalidCredentialsAndProtectedTargets(t *testing.T) {
	fake := testManagedInventory()
	svc := newManagedService(t, fake, t.TempDir())
	handler := svc.Handler()
	key := svc.ManagementKey
	path := managementPrefix + "/file-ingest/enabled"
	t.Run("unsigned", func(t *testing.T) {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest("POST", path, strings.NewReader(`{"enabled":true}`)))
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d", res.Code)
		}
	})
	t.Run("valid signature cannot be replayed", func(t *testing.T) {
		req := signedManagedRequest(t, key, "GET", managementPrefix, "")
		first := httptest.NewRecorder()
		handler.ServeHTTP(first, req)
		if first.Code != http.StatusOK {
			t.Fatalf("first request status=%d body=%s", first.Code, first.Body.String())
		}
		second := httptest.NewRecorder()
		duplicate := httptest.NewRequest("GET", managementPrefix, nil)
		duplicate.Header = req.Header.Clone()
		handler.ServeHTTP(second, duplicate)
		if second.Code != http.StatusUnauthorized {
			t.Fatalf("replayed management request status=%d", second.Code)
		}
		third := httptest.NewRecorder()
		handler.ServeHTTP(third, signedManagedRequest(t, key, "GET", managementPrefix, ""))
		if third.Code != http.StatusOK {
			t.Fatalf("fresh signed request status=%d", third.Code)
		}
	})
	t.Run("tampered body", func(t *testing.T) {
		req := signedManagedRequest(t, key, "POST", path, `{"enabled":false}`)
		req.Body = ioNopCloser(`{"enabled":true}`)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d", res.Code)
		}
	})
	t.Run("expired signature", func(t *testing.T) {
		req := signedManagedRequest(t, key, "GET", managementPrefix, "")
		req.Header.Set(managementTimestampHeader, "1000")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d", res.Code)
		}
	})
	t.Run("protected app", func(t *testing.T) {
		req := signedManagedRequest(t, key, "POST", managementPrefix+"/sample-app/enabled", `{"enabled":true}`)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusForbidden {
			t.Fatalf("code=%d", res.Code)
		}
	})
	t.Run("unknown app", func(t *testing.T) {
		req := signedManagedRequest(t, key, "POST", managementPrefix+"/missing/enabled", `{"enabled":true}`)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusNotFound {
			t.Fatalf("code=%d", res.Code)
		}
	})
}

func ioNopCloser(value string) *nopReader { return &nopReader{bytes.NewBufferString(value)} }

type nopReader struct{ *bytes.Buffer }

func (n *nopReader) Close() error { return nil }

func TestBootstrapManagerUsesExistingDeployerCredential(t *testing.T) {
	fake := testManagedInventory()
	root := t.TempDir()
	svc := newManagedService(t, fake, t.TempDir())
	server := httptest.NewServer(svc.Handler())
	defer server.Close()

	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets", "truenas-api-key"), svc.ManagementKey, 0600); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(bootstrapState{DeployerURL: server.URL, ConfiguredAt: time.Now()})
	if err := os.WriteFile(filepath.Join(root, "state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	manager := &BootstrapManager{Root: root, HTTP: server.Client()}
	if err := manager.SetManagedApp(context.Background(), "jellyfin", true); err != nil {
		t.Fatal(err)
	}
	apps, err := manager.ManagedApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, app := range apps {
		if app.Name == "jellyfin" {
			found = app.Enabled
		}
	}
	if !found {
		t.Fatal("manager did not enable jellyfin through the standalone deployer")
	}
	if err := manager.RedeployManagedApp(context.Background(), "jellyfin"); err != nil {
		t.Fatal(err)
	}
}

func TestManagedAppsProtectRunnerHubControlPlaneWithoutWorkflowTarget(t *testing.T) {
	fake := testManagedInventory()
	fake.inventory = append(fake.inventory,
		TrueNASApp{Name: "runnerhub", State: "RUNNING"},
		TrueNASApp{Name: "runnerhub-control", State: "RUNNING"},
	)
	svc := newManagedService(t, fake, t.TempDir())
	// The policy fixture contains another workflow target, not RunnerHub.
	// The control app must remain protected even without a matching target.
	for _, app := range []string{"runnerhub", "runnerhub-control"} {
		t.Run(app, func(t *testing.T) {
			if !svc.protectedApp(app) {
				t.Fatal("control-plane app must never use manual redeployment")
			}
			path := managementPrefix + "/" + app + "/enabled"
			res := httptest.NewRecorder()
			svc.Handler().ServeHTTP(res,
				signedManagedRequest(t, svc.ManagementKey, "POST", path, `{"enabled":true}`))
			if res.Code != http.StatusForbidden {
				t.Fatalf("enable status=%d, want 403", res.Code)
			}
		})
	}
}

func TestManagedAppPinnedRedeployRequiresAuthenticationAndEnabledApp(t *testing.T) {
	fake := testManagedInventory()
	for i := range fake.inventory {
		if fake.inventory[i].Name == "file-ingest" {
			fake.inventory[i].Images = []string{"ghcr.io/eutelic/file-ingest:latest"}
		}
	}
	svc := newManagedService(t, fake, t.TempDir())
	path := managementPrefix + "/file-ingest/redeploy-pinned"
	payload := `{"image":"ghcr.io/eutelic/file-ingest:latest","previousDigest":"` + baselineDigest + `","targetDigest":"` + updatedDigest + `"}`
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, signedManagedRequest(t, svc.ManagementKey, "POST", path, payload))
	if response.Code != http.StatusForbidden || len(fake.apps) != 0 {
		t.Fatalf("disabled pinned app deployed: status=%d apps=%v", response.Code, fake.apps)
	}
	if err := svc.setManaged("file-ingest", true); err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	unauthorizedReq := httptest.NewRequest("POST", path, strings.NewReader(payload))
	svc.Handler().ServeHTTP(unauthorized, unauthorizedReq)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned pinned redeploy accepted: %d", unauthorized.Code)
	}
	valid := httptest.NewRecorder()
	svc.Handler().ServeHTTP(valid, signedManagedRequest(t, svc.ManagementKey, "POST", path, payload))
	if valid.Code != http.StatusOK || len(fake.apps) != 1 {
		t.Fatalf("pinned deployment status=%d body=%s apps=%v", valid.Code, valid.Body.String(), fake.apps)
	}
	if fake.inventory[2].Images[0] != "ghcr.io/eutelic/file-ingest@"+updatedDigest {
		t.Fatalf("expected pinned inventory, got %v", fake.inventory[2].Images)
	}
	stale := httptest.NewRecorder()
	svc.Handler().ServeHTTP(stale, signedManagedRequest(t, svc.ManagementKey, "POST", path, payload))
	if stale.Code != http.StatusConflict || len(fake.apps) != 1 {
		t.Fatalf("accepted pinned deployment with outdated source: %d apps=%v", stale.Code, fake.apps)
	}
}
