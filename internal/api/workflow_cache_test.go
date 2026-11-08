package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
)

type countingWorkflowBackend struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	jobs  []githubrunner.WorkflowJob
}

func (b *countingWorkflowBackend) GenerateJITConfig(context.Context, githubrunner.JITConfigRequest) (githubrunner.JITConfig, error) {
	return githubrunner.JITConfig{}, nil
}

func (b *countingWorkflowBackend) GetRunner(context.Context, core.GitHubScope, int64) (githubrunner.Runner, error) {
	return githubrunner.Runner{}, nil
}

func (b *countingWorkflowBackend) DeleteRunner(context.Context, core.GitHubScope, int64) error {
	return nil
}

func (b *countingWorkflowBackend) ListWorkflowJobs(context.Context, core.GitHubScope, int64) ([]githubrunner.WorkflowJob, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	return append([]githubrunner.WorkflowJob(nil), b.jobs...), nil
}

func (b *countingWorkflowBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func TestCachedWorkflowJobsCoalescesConcurrentRequests(t *testing.T) {
	backend := &countingWorkflowBackend{
		delay: 50 * time.Millisecond,
		jobs:  []githubrunner.WorkflowJob{{ID: 1, Name: "test", Status: "in_progress"}},
	}
	s := &Service{GitHub: backend}
	scope := core.GitHubScope{Kind: core.ScopeRepository, Owner: "eutelic", Repository: "runnerhub"}

	const callers = 8
	var wg sync.WaitGroup
	wg.Add(callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			_, err := s.cachedWorkflowJobs(context.Background(), scope, 123)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := backend.callCount(); got != 1 {
		t.Fatalf("GitHub workflow-job calls=%d, want 1", got)
	}

	if _, err := s.cachedWorkflowJobs(context.Background(), scope, 123); err != nil {
		t.Fatal(err)
	}
	if got := backend.callCount(); got != 1 {
		t.Fatalf("cached request triggered another GitHub call: %d", got)
	}
}

func TestWorkflowJobsCacheTTL(t *testing.T) {
	active := workflowJobsCacheTTL([]githubrunner.WorkflowJob{{Status: "in_progress"}})
	if active != 30*time.Second {
		t.Fatalf("active TTL=%s, want 30s", active)
	}
	terminal := workflowJobsCacheTTL([]githubrunner.WorkflowJob{{Status: "completed"}})
	if terminal != 10*time.Minute {
		t.Fatalf("terminal TTL=%s, want 10m", terminal)
	}
}
