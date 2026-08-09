package core

import "testing"

func TestWorkerHappyPath(t *testing.T) {
	path := []WorkerState{
		WorkerRequested,
		WorkerProvisioning,
		WorkerStarting,
		WorkerRegistering,
		WorkerReady,
		WorkerBusy,
		WorkerCompleted,
		WorkerDeleting,
		WorkerDeleted,
	}
	for i := 0; i < len(path)-1; i++ {
		if !CanTransitionWorker(path[i], path[i+1]) {
			t.Fatalf("expected transition %s -> %s", path[i], path[i+1])
		}
	}
}

func TestWorkerCannotLeaveDeleted(t *testing.T) {
	if CanTransitionWorker(WorkerDeleted, WorkerRequested) {
		t.Fatal("deleted worker must be terminal")
	}
}

func TestValidateRunnerPool(t *testing.T) {
	pool := RunnerPool{
		Name:           "linux-small",
		Scope:          GitHubScope{Kind: ScopeRepository, Owner: "example-user", Repository: "runnerhub"},
		Labels:         []string{"linux-small"},
		Isolation:      IsolationContainerTrusted,
		MaxConcurrency: 4,
		MinIdle:        0,
	}
	if err := ValidateRunnerPool(pool); err != nil {
		t.Fatalf("expected valid pool, got %v", err)
	}
}

func TestValidateRunnerPoolRejectsUnsafeShape(t *testing.T) {
	pool := RunnerPool{
		Name:           "Linux Small",
		Scope:          GitHubScope{Kind: ScopeRepository, Owner: "example-user", Repository: "runnerhub"},
		Labels:         []string{"linux-small"},
		Isolation:      IsolationContainerTrusted,
		MaxConcurrency: 1,
	}
	if err := ValidateRunnerPool(pool); err == nil {
		t.Fatal("expected invalid pool name")
	}
}

func TestValidateRunnerPoolRejectsDuplicateLabels(t *testing.T) {
	pool := RunnerPool{
		Name:           "linux-small",
		Scope:          GitHubScope{Kind: ScopeRepository, Owner: "example-user", Repository: "runnerhub"},
		Labels:         []string{"GPU", "gpu"},
		Isolation:      IsolationContainerTrusted,
		MaxConcurrency: 1,
	}
	if err := ValidateRunnerPool(pool); err == nil {
		t.Fatal("expected duplicate labels to fail")
	}
}
