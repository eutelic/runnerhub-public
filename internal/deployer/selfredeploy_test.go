package deployer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type maintenanceFake struct {
	mu          sync.Mutex
	paused      bool
	workers     int
	resumeCount int
}

func (m *maintenanceFake) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch r.URL.Path {
	case "/healthz":
		w.WriteHeader(200)
		return
	case "/api/v1/maintenance/pause":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		m.paused = true
	case "/api/v1/maintenance/resume":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		if !m.paused {
			w.WriteHeader(409)
			return
		}
		m.paused = false
		m.resumeCount++
	case "/api/v1/maintenance":
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
	default:
		w.WriteHeader(404)
		return
	}
	phase := "running"
	if m.paused {
		phase = "draining"
		if m.workers == 0 {
			phase = "ready_to_redeploy"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"paused": m.paused, "generation": "generation-1", "activeWorkers": m.workers, "phase": phase,
	})
}

func selfPolicy(t *testing.T, controlURL string) *PolicySet {
	t.Helper()
	sum := sha256.Sum256([]byte("workflow-key"))
	p, err := NewPolicySet(Policy{Targets: []Target{{
		ID: "runnerhub", Repository: "eutelic/runnerhub", App: "runnerhub",
		Image:        "ghcr.io/eutelic/runnerhub-control:sha-test",
		TokenSHA256:  hex.EncodeToString(sum[:]),
		SelfRedeploy: true, MaintenanceURL: controlURL, HealthURL: controlURL + "/healthz",
		TimeoutSeconds: 5, DrainTimeoutSeconds: 30,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func requestSelfRedeploy(handler http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/runnerhub",
		bytes.NewBufferString("{\"sha\":\"0123456789abcdef0123456789abcdef01234567\",\"run_id\":\"1234\",\"image_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}"))
	req.Header.Set("Authorization", "Bearer workflow-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func pollSelfResult(t *testing.T, s *Service, want string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		result, err := s.readSelfStatus("runnerhub")
		if err == nil && result.Result == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	result, _ := s.readSelfStatus("runnerhub")
	t.Fatalf("did not observe self-redeployment status %q: %+v", want, result)
}

func TestSelfRedeployWaitsForExistingCIAndResumes(t *testing.T) {
	m := &maintenanceFake{workers: 2}
	control := httptest.NewServer(http.HandlerFunc(m.serve))
	defer control.Close()
	nas := &fakeTrueNAS{}
	s := &Service{Policy: selfPolicy(t, control.URL), TrueNAS: nas,
		MaintenanceToken: "trusted-admin-key",
		AuditPath:        filepath.Join(t.TempDir(), "audits.jsonl")}
	handler := s.Handler()
	start := time.Now()
	rec := requestSelfRedeploy(handler)
	if rec.Code != 202 {
		t.Fatalf("redeploy request %d: %s", rec.Code, rec.Body.String())
	}
	if time.Since(start) > time.Second {
		t.Fatal("self-redeployment request did not return promptly")
	}
	pollSelfResult(t, s, "draining")
	nas.mu.Lock()
	n := len(nas.apps)
	nas.mu.Unlock()
	if n != 0 {
		t.Fatal("TrueNAS was called before active CI finished")
	}
	// Repeated requests must not start a second transaction.
	dup := requestSelfRedeploy(handler)
	if dup.Code != 409 {
		t.Fatalf("duplicate request %d", dup.Code)
	}
	m.mu.Lock()
	m.workers = 0
	m.mu.Unlock()
	pollSelfResult(t, s, "success")
	nas.mu.Lock()
	n = len(nas.apps)
	image := ""
	if len(nas.images) > 0 {
		image = nas.images[0]
	}
	nas.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected one TrueNAS redeploy, got %d", n)
	}
	if image != "ghcr.io/eutelic/runnerhub-control@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("redeployed image=%q", image)
	}
	m.mu.Lock()
	paused, resumes := m.paused, m.resumeCount
	m.mu.Unlock()
	if paused || resumes != 1 {
		t.Fatalf("admission not resumed: paused=%v resumes=%d", paused, resumes)
	}
}

type failingSelfTrueNAS struct{}

func (f failingSelfTrueNAS) RedeployAndWait(context.Context, string) error {
	return errors.New("immutable image update required")
}
func (f failingSelfTrueNAS) VerifyImmutableImageUpdate(context.Context, string, string) error {
	return nil
}
func (f failingSelfTrueNAS) RedeployImageAndWait(context.Context, string, string) error {
	return errors.New("simulated app startup failure")
}

func TestSelfRedeployFailureRequiresRecoveryAndKeepsPause(t *testing.T) {
	m := &maintenanceFake{}
	control := httptest.NewServer(http.HandlerFunc(m.serve))
	defer control.Close()
	s := &Service{Policy: selfPolicy(t, control.URL), TrueNAS: failingSelfTrueNAS{},
		MaintenanceToken: "trusted-admin-key",
		AuditPath:        filepath.Join(t.TempDir(), "audits.jsonl")}
	rec := requestSelfRedeploy(s.Handler())
	if rec.Code != 202 {
		t.Fatalf("request %d: %s", rec.Code, rec.Body.String())
	}
	pollSelfResult(t, s, "recovery_required")
	m.mu.Lock()
	paused, resumes := m.paused, m.resumeCount
	m.mu.Unlock()
	if !paused || resumes != 0 {
		t.Fatalf("failed deployment resumed jobs: paused=%v resumes=%d", paused, resumes)
	}
}

func TestInterruptedSelfRedeployRequiresManualAdmissionRecovery(t *testing.T) {
	m := &maintenanceFake{paused: true, workers: 0}
	control := httptest.NewServer(http.HandlerFunc(m.serve))
	defer control.Close()
	svc := &Service{Policy: selfPolicy(t, control.URL), TrueNAS: &fakeTrueNAS{},
		MaintenanceToken: "trusted-admin-key",
		AuditPath:        filepath.Join(t.TempDir(), "audits.jsonl")}
	stale := deploymentRecord{ID: "old-op", Target: "runnerhub", App: "runnerhub", Result: "draining"}
	if err := svc.writeSelfStatus(stale); err != nil {
		t.Fatal(err)
	}
	// Simulate a process restart: new service instance has no in-memory lock
	// ownership, but the stored record and maintenance pause survive.
	restarted := &Service{Policy: svc.Policy, TrueNAS: &fakeTrueNAS{},
		MaintenanceToken: "trusted-admin-key", AuditPath: svc.AuditPath}
	conflict := requestSelfRedeploy(restarted.Handler())
	if conflict.Code != 409 {
		t.Fatalf("retry bypassed paused recovery: %d", conflict.Code)
	}
	m.mu.Lock()
	m.paused = false // administrator has explicitly resumed the persisted gate
	m.mu.Unlock()
	accepted := requestSelfRedeploy(restarted.Handler())
	if accepted.Code != 202 {
		t.Fatalf("retry was still blocked after safe recovery: %d %s", accepted.Code, accepted.Body.String())
	}
	pollSelfResult(t, restarted, "success")
}

type lostPauseResponseTransport struct{ base http.RoundTripper }

func (t lostPauseResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if req.Method == http.MethodPost && req.URL.Path == "/api/v1/maintenance/pause" {
		_ = resp.Body.Close()
		return nil, errors.New("simulated lost pause response after persistence")
	}
	return resp, nil
}

func TestSelfRedeployLostPauseResponseRequiresRecovery(t *testing.T) {
	m := &maintenanceFake{}
	control := httptest.NewServer(http.HandlerFunc(m.serve))
	defer control.Close()
	nas := &fakeTrueNAS{}
	svc := &Service{Policy: selfPolicy(t, control.URL), TrueNAS: nas,
		MaintenanceToken: "trusted-admin-key",
		HTTPClient:       &http.Client{Transport: lostPauseResponseTransport{base: http.DefaultTransport}},
		AuditPath:        filepath.Join(t.TempDir(), "audits.jsonl")}
	accepted := requestSelfRedeploy(svc.Handler())
	if accepted.Code != 202 {
		t.Fatalf("request %d: %s", accepted.Code, accepted.Body.String())
	}
	pollSelfResult(t, svc, "recovery_required")
	m.mu.Lock()
	paused, resumes := m.paused, m.resumeCount
	m.mu.Unlock()
	if !paused || resumes != 0 {
		t.Fatalf("lost response incorrectly released admission: paused=%v resumes=%d", paused, resumes)
	}
	nas.mu.Lock()
	count := len(nas.apps)
	nas.mu.Unlock()
	if count != 0 {
		t.Fatalf("redeployed after ambiguous maintenance pause: %d", count)
	}
}

type deniedPreflightTrueNAS struct{ fakeTrueNAS }

func (f *deniedPreflightTrueNAS) VerifyImmutableImageUpdate(context.Context, string, string) error {
	return errors.New("FULL_ADMIN role is required for custom Compose updates")
}
func TestSelfRedeployCapabilityFailureLeavesAdmissionsOpen(t *testing.T) {
	state := &maintenanceFake{workers: 0}
	control := httptest.NewServer(http.HandlerFunc(state.serve))
	defer control.Close()
	nas := &deniedPreflightTrueNAS{}
	svc := &Service{Policy: selfPolicy(t, control.URL), TrueNAS: nas,
		MaintenanceToken: "trusted-admin-key", AuditPath: filepath.Join(t.TempDir(), "audits.jsonl")}
	response := requestSelfRedeploy(svc.Handler())
	if response.Code != 202 {
		t.Fatalf("expected accepted async request, got %d", response.Code)
	}
	pollSelfResult(t, svc, "failed")
	state.mu.Lock()
	paused := state.paused
	resumes := state.resumeCount
	state.mu.Unlock()
	if paused || resumes != 0 {
		t.Fatalf("preflight failure altered maintenance state: paused=%v resumes=%d", paused, resumes)
	}
	nas.mu.Lock()
	calls := len(nas.apps)
	nas.mu.Unlock()
	if calls != 0 {
		t.Fatalf("TrueNAS redeployed without permission preflight: %d", calls)
	}
}
