package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		if got := r.URL.Query().Get("excludeDeleted"); got != "true" {
			t.Fatalf("scale-set worker recovery did not request the indexed snapshot: %q", got)
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

func TestListenerRetryDelayIsBoundedAndJittered(t *testing.T) {
	first := listenerRetryDelay(1, "pool-a")
	if first < 10*time.Second || first > 12500*time.Millisecond {
		t.Fatalf("first delay=%s outside expected jitter window", first)
	}
	second := listenerRetryDelay(2, "pool-a")
	if second < 20*time.Second || second > 25*time.Second {
		t.Fatalf("second delay=%s outside expected jitter window", second)
	}
	if got := listenerRetryDelayWithJitter(1, func(int64) int64 { return 0 }); got != 10*time.Second {
		t.Fatalf("zero jitter delay=%s, want 10s", got)
	}
	if got := listenerRetryDelayWithJitter(1, func(n int64) int64 { return n - 1 }); got != 12500*time.Millisecond {
		t.Fatalf("maximum jitter delay=%s, want 12.5s", got)
	}
	if got := listenerRetryDelay(20, "pool-a"); got > 5*time.Minute {
		t.Fatalf("retry delay exceeded cap: %s", got)
	}
}

func TestChangedListenerRetiresBeforeReplacementCanStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelCalls := 0
	active := map[string]managedListener{
		"pool-a": {
			cancel: func() {
				cancelCalls++
				cancel()
			},
			fingerprint: "old-config",
			ready:       true,
		},
	}

	if !beginListenerStop(active, "pool-a") {
		t.Fatal("changed listener was not marked stopping")
	}
	running := active["pool-a"]
	if !running.stopping || running.ready {
		t.Fatalf("retiring listener state=%+v, want stopping and not ready", running)
	}
	if listenerMayStart("pool-a", active) {
		t.Fatal("replacement listener could start before old message session closed")
	}
	if beginListenerStop(active, "pool-a") {
		t.Fatal("already stopping listener was canceled twice")
	}
	if cancelCalls != 1 {
		t.Fatalf("cancel calls=%d, want one", cancelCalls)
	}

	// The manager removes the old entry only after runListener returns, after
	// its deferred MessageSession.Close has run.
	delete(active, "pool-a")
	if !listenerMayStart("pool-a", active) {
		t.Fatal("replacement remained blocked after old listener exited")
	}
	if ctx.Err() == nil {
		t.Fatal("test listener cancellation was not observed")
	}
}

func TestFailedSessionCloseBlocksListenerReplacement(t *testing.T) {
	active := map[string]managedListener{
		"pool-a": {fingerprint: "old-config", ready: true},
	}
	resultErr := messageSessionCloseResult(context.Canceled, errors.New("broker returned conflict"))
	if !errors.Is(resultErr, context.Canceled) || !errors.Is(resultErr, errMessageSessionClose) {
		t.Fatalf("session close error did not preserve cancellation and close failure: %v", resultErr)
	}
	if !blockListenerOnSessionCloseFailure(active, "pool-a", resultErr) {
		t.Fatal("session close failure did not block the listener")
	}
	if listenerMayStart("pool-a", active) {
		t.Fatal("replacement listener started after message session close failed")
	}
	if running := active["pool-a"]; !running.stopping || running.ready {
		t.Fatalf("blocked listener state=%+v, want stopping and not ready", running)
	}
	if blockListenerOnSessionCloseFailure(active, "pool-a", errors.New("ordinary listener error")) {
		t.Fatal("ordinary listener error was mistaken for a session close failure")
	}
}

func TestRateLimitTrackerUsesResetAndRetryAfter(t *testing.T) {
	now := time.Now()
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header)}
	resp.Header.Set("X-RateLimit-Resource", "actions_runner_registration")
	resp.Header.Set("X-RateLimit-Limit", "100")
	resp.Header.Set("X-RateLimit-Remaining", "0")
	resp.Header.Set("X-RateLimit-Reset", fmt.Sprintf("%d", now.Add(2*time.Minute).Unix()))
	resp.Header.Set("Retry-After", "30")
	resp.Header.Set("X-GitHub-Request-Id", "req-123")
	tracker := &githubRateLimitTracker{}
	tracker.observe(resp)
	until, ok := tracker.cooldown(now)
	if !ok || until.Before(now.Add(90*time.Second)) {
		t.Fatalf("cooldown=%s ok=%v, expected reset-based cooldown", until, ok)
	}
	detail := tracker.detail(now)
	for _, want := range []string{"resource=actions_runner_registration", "remaining=0/100", "requestId=req-123"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail=%q missing %q", detail, want)
		}
	}
}

func TestScaleSetClientConstructionDoesNotPanicOnCustomRetries(t *testing.T) {
	// The SDK validates GitHub App credentials during client construction.
	// Use a real ephemeral RSA key so this test exercises the retry transport
	// rather than failing prematurely on an invalid placeholder credential.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test GitHub App key: %v", err)
	}
	privateKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	cfg := config{
		RegistrationURL:   "https://github.com/eutelic/file-ingest",
		AppClientID:       "Iv1.test",
		AppInstallationID: 123,
		AppPrivateKey:     string(privateKey),
		rateLimit:         &githubRateLimitTracker{},
	}
	client, err := cfg.client()
	if err != nil {
		t.Fatalf("construct scale-set client: %v", err)
	}
	if client == nil {
		t.Fatal("scale-set client is nil")
	}
	if _, ok := newGitHubRetryClient(cfg.rateLimit).HTTPClient.Transport.(*http.Transport); !ok {
		t.Fatal("retry client must preserve concrete *http.Transport for actions/scaleset")
	}
}

func TestGitHubRetryClientTracksRateLimitWithoutTransportWrapper(t *testing.T) {
	tracker := &githubRateLimitTracker{}
	client := newGitHubRetryClient(tracker)
	now := time.Now()
	limited := &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header)}
	limited.Header.Set("X-RateLimit-Remaining", "0")
	limited.Header.Set("X-RateLimit-Reset", fmt.Sprintf("%d", now.Add(2*time.Minute).Unix()))
	limited.Header.Set("X-GitHub-Request-Id", "retry-test")

	retry, err := client.CheckRetry(context.Background(), limited, nil)
	if err != nil || !retry {
		t.Fatalf("rate-limited GitHub 403: retry=%v err=%v", retry, err)
	}
	if until, ok := tracker.cooldown(now); !ok || until.Before(now.Add(time.Minute)) {
		t.Fatalf("missing shared installation cooldown: until=%v active=%v", until, ok)
	}
	if got := tracker.detail(now); !strings.Contains(got, "requestId=retry-test") {
		t.Fatalf("lost GitHub rate-limit metadata: %q", got)
	}

	forbidden := &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header)}
	retry, err = client.CheckRetry(context.Background(), forbidden, nil)
	if err != nil || retry {
		t.Fatalf("ordinary GitHub 403 should not retry: retry=%v err=%v", retry, err)
	}
	tooMany := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
	retry, err = client.CheckRetry(context.Background(), tooMany, nil)
	if err != nil || !retry {
		t.Fatalf("GitHub 429 should retry: retry=%v err=%v", retry, err)
	}
}

func TestGitHubCooldownWaitRespectsCancellation(t *testing.T) {
	tracker := &githubRateLimitTracker{}
	limited := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
	limited.Header.Set("Retry-After", "3600")
	tracker.observe(limited)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		waitForGitHubCooldown(ctx, tracker)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled request remained blocked by GitHub rate-limit cooldown")
	}
}

func TestListenerRetryStatusRetainsRootCause(t *testing.T) {
	retry := listenerRetryState{
		attempt:     3,
		nextAttempt: time.Date(2026, 11, 4, 23, 22, 0, 0, time.UTC),
		lastFailure: "handle started job: runnerhub /api/v1/scale-events returned 404: scale-set worker not found",
	}
	detail := listenerRetryStatus(retry)
	for _, want := range []string{"attempt=3", "2026-11-04T23:22:00Z", "scale-set worker not found"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("listener status lost %q: %q", want, detail)
		}
	}
	if got := listenerFailureSummary(fmt.Errorf("failed to handle stale event")); got != "listener operation failed (details withheld)" {
		t.Fatalf("unexpected failure classification: %q", got)
	}
	if got := listenerFailureSummary(fmt.Errorf("upstream 404 scale-set worker not found; Authorization: Bearer secret-token")); got != "scale-set worker not found (404)" {
		t.Fatalf("unsafe stale worker classification: %q", got)
	}
	for _, detail := range []string{"409 conflict", "403 forbidden", "429 too many requests", "network failure"} {
		got := listenerFailureSummary(fmt.Errorf("%s token=super-secret", detail))
		if strings.Contains(got, "super-secret") || strings.Contains(got, "token=") {
			t.Fatalf("credential leaked into listener diagnostics: %q", got)
		}
	}
	if got := listenerFailureSummary(nil); got != "" {
		t.Fatalf("nil failure summary = %q", got)
	}
}

func TestScaleLateStartedJobDoesNotCrashListener(t *testing.T) {
	tests := []struct {
		name          string
		eventStatus   int
		workerStatus  int
		activeWorkers []workerResponse
		wantError     bool
		wantLookup    bool
		wantTracked   bool
	}{
		{name: "stale missing worker", eventStatus: http.StatusNotFound, workerStatus: http.StatusOK, wantLookup: true},
		{
			name:          "404 but worker still active",
			eventStatus:   http.StatusNotFound,
			workerStatus:  http.StatusOK,
			activeWorkers: []workerResponse{{ID: "live", PoolID: "pool-docker", RunnerName: "runner-1", State: "busy"}},
			wantError:     true, wantLookup: true, wantTracked: true,
		},
		{
			name:         "404 and lookup unavailable",
			eventStatus:  http.StatusNotFound,
			workerStatus: http.StatusInternalServerError,
			wantError:    true, wantLookup: true, wantTracked: true,
		},
		{name: "non 404 error", eventStatus: http.StatusInternalServerError, wantError: true, wantTracked: true},
		{name: "normal started job", eventStatus: http.StatusOK, wantTracked: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var events, lookups int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/scale-events":
					events++
					if r.Method != http.MethodPost {
						t.Errorf("wrong event method: %s", r.Method)
					}
					var ev scaleEvent
					if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
						t.Errorf("decode event: %v", err)
					}
					if ev.PoolID != "pool-docker" || ev.RunnerName != "runner-1" || ev.State != "busy" {
						t.Errorf("wrong event: %+v", ev)
					}
					w.WriteHeader(tc.eventStatus)
					if tc.eventStatus != http.StatusOK {
						_, _ = w.Write([]byte(`{"detail":"scale-set worker not found"}`))
					}
				case "/api/v1/workers":
					lookups++
					if r.URL.Query().Get("excludeDeleted") != "true" {
						t.Errorf("missing indexed active-worker filter")
					}
					if tc.workerStatus != http.StatusOK {
						w.WriteHeader(tc.workerStatus)
						return
					}
					_ = json.NewEncoder(w).Encode(tc.activeWorkers)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			sc := &scaler{
				names:  map[string]string{"runner-1": "old-worker"},
				hub:    runnerHubClient{base: srv.URL, poolID: "pool-docker", http: srv.Client()},
				logger: slog.Default(),
			}
			message := &scaleset.RunnerScaleSetMessage{
				JobStartedMessages: []*scaleset.JobStarted{{RunnerName: "runner-1", WorkflowRunID: 123}},
			}
			err := sc.Scale(context.Background(), message)
			if (err != nil) != tc.wantError {
				t.Fatalf("Scale err=%v, want error=%v", err, tc.wantError)
			}
			if events != 1 {
				t.Errorf("event requests=%d, want 1", events)
			}
			if (lookups != 0) != tc.wantLookup {
				t.Errorf("active-worker lookups=%d, want lookup=%v", lookups, tc.wantLookup)
			}
			sc.mu.Lock()
			_, tracked := sc.names["runner-1"]
			sc.mu.Unlock()
			if tracked != tc.wantTracked {
				t.Errorf("tracked=%v, want %v", tracked, tc.wantTracked)
			}
		})
	}
}
