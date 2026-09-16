package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestSyncActiveWorkersDropsFailedAndRecoversLiveWorkers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workers" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer admin" {
			t.Fatalf("authorization=%q", got)
		}
		_ = json.NewEncoder(w).Encode([]workerResponse{
			{ID: "live-id", PoolID: "pool-docker", RunnerName: "live", State: "registering"},
			{ID: "failed-id", PoolID: "pool-docker", RunnerName: "failed", State: "failed"},
			{ID: "other-id", PoolID: "other", RunnerName: "other", State: "busy"},
		})
	}))
	defer srv.Close()

	s := &scaler{
		names: map[string]string{"stale": "stale-id"},
		hub: runnerHubClient{
			base:   srv.URL,
			token:  "admin",
			poolID: "pool-docker",
			http:   srv.Client(),
		},
	}
	if err := s.syncActiveWorkers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.count() != 1 {
		t.Fatalf("active count=%d names=%v", s.count(), s.names)
	}
	if got := s.names["live"]; got != "live-id" {
		t.Fatalf("live worker not recovered: %v", s.names)
	}
	if _, ok := s.names["stale"]; ok {
		t.Fatalf("stale local worker was retained: %v", s.names)
	}
	if _, ok := s.names["failed"]; ok {
		t.Fatalf("failed RunnerHub worker was counted as active: %v", s.names)
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


func TestAllDesiredListenersReady(t *testing.T) {
	if !allDesiredListenersReady(map[string]managedConfig{}, map[string]managedListener{}) {
		t.Fatal("zero desired listeners should be ready")
	}
	desired := map[string]managedConfig{"pool-a": {PoolID: "pool-a"}}
	if allDesiredListenersReady(desired, map[string]managedListener{}) {
		t.Fatal("missing listener reported ready")
	}
	active := map[string]managedListener{"pool-a": {ready: false}}
	if allDesiredListenersReady(desired, active) {
		t.Fatal("starting listener reported ready")
	}
	active["pool-a"] = managedListener{ready: true}
	if !allDesiredListenersReady(desired, active) {
		t.Fatal("ready listener was not recognized")
	}
	desired["pool-b"] = managedConfig{PoolID: "pool-b"}
	if allDesiredListenersReady(desired, active) {
		t.Fatal("partially ready listener set reported ready")
	}
}

func TestScaleSetStatusPostsPoolState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/scalesets/status" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer admin" {
			t.Fatalf("authorization=%q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["poolId"] != "pool-a" || body["state"] != "error" || body["detail"] != "boom" {
			t.Fatalf("body=%v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := runnerHubClient{base: srv.URL, token: "admin", http: srv.Client()}
	if err := client.scaleSetStatus(context.Background(), "pool-a", "error", "boom"); err != nil {
		t.Fatal(err)
	}
}


type fakeScaleSetRunnerClient struct {
	generateCalls int
	removeCalls   []int64
	jit           *scaleset.RunnerScaleSetJitRunnerConfig
	generateErr   error
	removeErr     error
}

func (f *fakeScaleSetRunnerClient) GenerateJitRunnerConfig(_ context.Context, _ *scaleset.RunnerScaleSetJitRunnerSetting, _ int) (*scaleset.RunnerScaleSetJitRunnerConfig, error) {
	f.generateCalls++
	if f.generateErr != nil {
		return nil, f.generateErr
	}
	return f.jit, nil
}

func (f *fakeScaleSetRunnerClient) RemoveRunner(_ context.Context, id int64) error {
	f.removeCalls = append(f.removeCalls, id)
	return f.removeErr
}

func TestHandleDesiredRunnerCountWaitsForCapacityBeforeGeneratingJIT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workers":
			_ = json.NewEncoder(w).Encode([]workerResponse{})
		case "/api/v1/pools/pool-docker/health":
			_ = json.NewEncoder(w).Encode(poolHealthResponse{Healthy: false, Reasons: []string{"no online compatible hosts with requested capacity"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	gh := &fakeScaleSetRunnerClient{
		jit: &scaleset.RunnerScaleSetJitRunnerConfig{
			Runner:           &scaleset.RunnerReference{ID: 123, Name: "runner"},
			EncodedJITConfig: "jit",
		},
	}
	s := &scaler{
		names:      map[string]string{},
		min:        0,
		max:        2,
		scaleSetID: 7,
		github:     gh,
		hub: runnerHubClient{
			base:   srv.URL,
			poolID: "pool-docker",
			http:   srv.Client(),
		},
		logger: slog.Default(),
	}
	got, err := s.HandleDesiredRunnerCount(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("active runners=%d, want 0", got)
	}
	if gh.generateCalls != 0 {
		t.Fatalf("generated %d JIT runners despite no capacity", gh.generateCalls)
	}
}

func TestHandleDesiredRunnerCountCleansJITRunnerOnCapacityRace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workers":
			_ = json.NewEncoder(w).Encode([]workerResponse{})
		case "/api/v1/pools/pool-docker/health":
			_ = json.NewEncoder(w).Encode(poolHealthResponse{Healthy: true})
		case "/api/v1/pools/pool-docker/scale-workers":
			http.Error(w, `{"detail":"no compatible host capacity: resources exhausted"}`, http.StatusConflict)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	gh := &fakeScaleSetRunnerClient{
		jit: &scaleset.RunnerScaleSetJitRunnerConfig{
			Runner:           &scaleset.RunnerReference{ID: 456, Name: "runner"},
			EncodedJITConfig: "jit",
		},
	}
	s := &scaler{
		names:      map[string]string{},
		min:        0,
		max:        2,
		scaleSetID: 7,
		github:     gh,
		hub: runnerHubClient{
			base:   srv.URL,
			poolID: "pool-docker",
			http:   srv.Client(),
		},
		logger: slog.Default(),
	}
	got, err := s.HandleDesiredRunnerCount(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("active runners=%d, want 0", got)
	}
	if gh.generateCalls != 1 {
		t.Fatalf("generated JIT calls=%d, want 1", gh.generateCalls)
	}
	if len(gh.removeCalls) != 1 || gh.removeCalls[0] != 456 {
		t.Fatalf("removed runners=%v, want [456]", gh.removeCalls)
	}
}

func TestRunnerHubClientCreateSendsGitHubRunnerID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pools/pool-docker/scale-workers" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			RunnerName     string `json:"runnerName"`
			JITConfig      string `json:"jitConfig"`
			GitHubRunnerID int64  `json:"githubRunnerId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.RunnerName != "runner-1" || body.JITConfig != "jit" || body.GitHubRunnerID != 789 {
			t.Fatalf("body=%+v", body)
		}
		_ = json.NewEncoder(w).Encode(workerResponse{ID: "worker-1", PoolID: "pool-docker", RunnerName: body.RunnerName, State: "provisioning"})
	}))
	defer srv.Close()

	client := runnerHubClient{base: srv.URL, poolID: "pool-docker", http: srv.Client()}
	got, err := client.create(context.Background(), "runner-1", "jit", 789)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "worker-1" {
		t.Fatalf("worker=%+v", got)
	}
}
