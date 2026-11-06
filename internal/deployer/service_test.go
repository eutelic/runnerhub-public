package deployer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeTrueNAS struct {
	mu    sync.Mutex
	apps  []string
	images []string
	block chan struct{}
}

func (f *fakeTrueNAS) RedeployAndWait(ctx context.Context, app string) error {
	f.mu.Lock()
	f.apps = append(f.apps, app)
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.block:
		}
	}
	return nil
}

func (f *fakeTrueNAS) RedeployImageAndWait(ctx context.Context, app, image string) error {
	f.mu.Lock()
	f.images = append(f.images, image)
	f.mu.Unlock()
	return f.RedeployAndWait(ctx, app)
}

func testPolicy(t *testing.T, token string, healthURL string) *PolicySet {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	p, err := NewPolicySet(Policy{Targets: []Target{{
		ID:             "sample-app",
		Repository:     "example-org/sample-app",
		App:            "sample-app",
		Image:          "ghcr.io/example-org/sample-app:latest",
		TokenSHA256:    hex.EncodeToString(sum[:]),
		HealthURL:      healthURL,
		TimeoutSeconds: 5,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func deployRequest(token string) *http.Request {
	body := []byte(`{"sha":"0123456789abcdef0123456789abcdef01234567","run_id":"123456789"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/sample-app", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestDeploymentUsesServerSideTarget(t *testing.T) {
	fake := &fakeTrueNAS{}
	svc := &Service{
		Policy:    testPolicy(t, "secret-token", ""),
		TrueNAS:   fake,
		AuditPath: filepath.Join(t.TempDir(), "audit.jsonl"),
	}
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, deployRequest("secret-token"))
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.apps) != 1 || fake.apps[0] != "sample-app" {
		t.Fatalf("redeployed apps=%v", fake.apps)
	}
}

func TestDeploymentRejectsBadToken(t *testing.T) {
	svc := &Service{
		Policy:    testPolicy(t, "secret-token", ""),
		TrueNAS:   &fakeTrueNAS{},
		AuditPath: filepath.Join(t.TempDir(), "audit.jsonl"),
	}
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, deployRequest("wrong"))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", res.Code)
	}
}

func TestDeploymentRejectsMissingAuditMetadata(t *testing.T) {
	svc := &Service{
		Policy:    testPolicy(t, "secret-token", ""),
		TrueNAS:   &fakeTrueNAS{},
		AuditPath: filepath.Join(t.TempDir(), "audit.jsonl"),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/sample-app", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestDeploymentRunsConfiguredHealthCheckAndAudits(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()

	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	svc := &Service{
		Policy:    testPolicy(t, "secret-token", health.URL),
		TrueNAS:   &fakeTrueNAS{},
		AuditPath: audit,
	}
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, deployRequest("secret-token"))
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	data, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	var record deploymentRecord
	if err := json.Unmarshal(data[:len(data)-1], &record); err != nil {
		t.Fatal(err)
	}
	if record.Result != "success" ||
		record.App != "sample-app" ||
		record.Repository != "example-org/sample-app" ||
		record.Image != "ghcr.io/example-org/sample-app:latest" ||
		record.SHA != "0123456789abcdef0123456789abcdef01234567" ||
		record.RunID != "123456789" {
		t.Fatalf("record=%+v", record)
	}
}

func TestDeploymentSerializesPerApp(t *testing.T) {
	block := make(chan struct{})
	fake := &fakeTrueNAS{block: block}
	svc := &Service{
		Policy:    testPolicy(t, "secret-token", ""),
		TrueNAS:   fake,
		AuditPath: filepath.Join(t.TempDir(), "audit.jsonl"),
	}
	handler := svc.Handler()
	firstDone := make(chan int, 1)
	go func() {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, deployRequest("secret-token"))
		firstDone <- res.Code
	}()
	time.Sleep(50 * time.Millisecond)

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, deployRequest("secret-token"))
	if res.Code != http.StatusConflict {
		t.Fatalf("second deployment status=%d", res.Code)
	}
	close(block)
	if code := <-firstDone; code != http.StatusOK {
		t.Fatalf("first deployment status=%d", code)
	}
}


func TestShutdownWaitsForCanceledSelfRedeployRecovery(t *testing.T) {
	svc := &Service{}
	if !svc.beginSelfRedeploy() {
		t.Fatal("self-redeployment did not start before shutdown")
	}

	operationCtx, cancelOperation := context.WithCancel(context.Background())
	recoveryStarted := make(chan struct{})
	allowRecovery := make(chan struct{})
	recoveryFinished := make(chan struct{})
	go func() {
		defer svc.selfOps.Done()
		<-operationCtx.Done()
		close(recoveryStarted)
		<-allowRecovery
		close(recoveryFinished)
	}()

	svc.StopSelfRedeployAdmissions()
	if svc.beginSelfRedeploy() {
		t.Fatal("shutdown accepted a new self-redeployment")
	}
	cancelOperation()
	<-recoveryStarted

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 50*time.Millisecond)
	waitErr := svc.WaitForSelfRedeploys(waitCtx)
	cancelWait()
	if waitErr != context.DeadlineExceeded {
		t.Fatalf("shutdown wait error=%v, want deadline while recovery is still in progress", waitErr)
	}

	close(allowRecovery)
	<-recoveryFinished
	completeCtx, cancelComplete := context.WithTimeout(context.Background(), time.Second)
	defer cancelComplete()
	if err := svc.WaitForSelfRedeploys(completeCtx); err != nil {
		t.Fatalf("shutdown did not finish after bounded recovery completed: %v", err)
	}
}

func (f *fakeTrueNAS) VerifyImmutableImageUpdate(context.Context,string,string)error{return nil}
