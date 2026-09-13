package core

import "testing"

func TestWorkerHappyPath(t *testing.T) {
	path := []WorkerState{WorkerRequested, WorkerProvisioning, WorkerStarting, WorkerRegistering, WorkerReady, WorkerBusy, WorkerCompleted, WorkerDeleting, WorkerDeleted}
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
func validPool() RunnerPool {
	return RunnerPool{Name: "linux-small", Scope: GitHubScope{Kind: ScopeRepository, Owner: "eutelic", Repository: "runnerhub"}, Labels: []string{"linux-small"}, Isolation: IsolationContainerTrusted, MaxConcurrency: 4, RunnerGroupID: 1}
}
func TestValidateRunnerPool(t *testing.T) {
	if err := ValidateRunnerPool(validPool()); err != nil {
		t.Fatalf("expected valid pool, got %v", err)
	}
}
func TestValidateRunnerPoolAllowsContainerDinDShape(t *testing.T) {
	p := validPool()
	p.Isolation = IsolationContainerDinD
	if err := ValidateRunnerPool(p); err != nil {
		t.Fatalf("expected container-dind to be a valid core isolation mode, got %v", err)
	}
}
func TestValidateRunnerPoolRejectsUnsafeShape(t *testing.T) {
	p := validPool()
	p.Name = "Linux Small"
	if ValidateRunnerPool(p) == nil {
		t.Fatal("expected invalid pool name")
	}
}
func TestValidateRunnerPoolRejectsDuplicateLabels(t *testing.T) {
	p := validPool()
	p.Labels = []string{"GPU", "gpu"}
	if ValidateRunnerPool(p) == nil {
		t.Fatal("expected duplicate labels")
	}
}
func TestValidateRunnerPoolRequiresGroup(t *testing.T) {
	p := validPool()
	p.RunnerGroupID = 0
	if ValidateRunnerPool(p) == nil {
		t.Fatal("expected runner group requirement")
	}
}
