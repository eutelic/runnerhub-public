package api

import (
	"context"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/agent"
	"github.com/eutelic/runnerhub/internal/agenthub"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
	"github.com/eutelic/runnerhub/internal/scheduler"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestScaleAdmissionRedeliveryIsIdempotentAcrossControllerRestart(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(":memory:")
	if err != nil { t.Fatal(err) }
	defer db.Close()
	hub, err := agenthub.New(db, "0123456789abcdef", "0123456789abcdef0123456789abcdef")
	if err != nil { t.Fatal(err) }
	host, err := hub.Enroll(ctx, "0123456789abcdef", agent.EnrollRequest{
		Name: "compute", Provider: "docker", Capabilities: provider.Capabilities{
			Provider: "docker",
			Capacity: core.Resources{CPUMillis: 4000, MemoryBytes: 8 << 30, DiskBytes: 100 << 30},
			IsolationModes: []core.IsolationMode{core.IsolationContainerTrusted},
			Labels: map[string]string{"isolation.container-trusted":"true"},
		},
	})
	if err != nil || host.HostID == "" { t.Fatalf("enroll: %+v %v", host, err) }
	pool := core.RunnerPool{
		ID:"pool-docker", Name:"test", Scope:core.GitHubScope{Kind:core.ScopeRepository,Owner:"acme",Repository:"app"},
		Labels:[]string{"runnerhub"}, Isolation:core.IsolationContainerTrusted,
		Resources:core.Resources{CPUMillis:1000,MemoryBytes:1 << 30},
		MaxConcurrency:1, RunnerGroupID:1, Image:"img",
	}
	if err := db.UpsertPool(ctx,pool); err != nil { t.Fatal(err) }
	newService := func() *Service {
		return &Service{Store:db, Hub:hub, Scheduler:scheduler.Scheduler{Store:db}}
	}

	first, err := newService().enqueueWorker(ctx,pool,"jit-runner-abc",123,"encoded-jit","acme/app","")
	if err != nil { t.Fatal(err) }
	if first.ID == "" || first.HostID != host.HostID { t.Fatalf("first worker: %+v", first) }

	// A new control plane receives the same request after losing its response.
	restarted := newService()
	replay, err := restarted.enqueueWorker(ctx,pool,"jit-runner-abc",123,"encoded-jit","acme/app","")
	if err != nil { t.Fatalf("retry should return the previous admission: %v",err) }
	if replay.ID != first.ID { t.Fatalf("retry generated a duplicate worker %q != %q",replay.ID,first.ID) }

	// A second GitHub runner must never borrow a slot using an existing name.
	if _,err := restarted.enqueueWorker(ctx,pool,"jit-runner-abc",456,"different-jit","acme/app",""); err == nil ||
		!strings.Contains(err.Error(),"identity conflict") {
		t.Fatalf("different GitHub runner reused the admission: %v",err)
	}
	// Replayed assignments cannot create another worker even after cleanup
	// removes the old instance from current capacity accounting.
	first.State = core.WorkerDeleted
	if err := db.UpsertWorker(ctx,first); err != nil { t.Fatal(err) }
	deletedReplay,err := restarted.enqueueWorker(ctx,pool,"jit-runner-abc",123,"encoded-jit","acme/app","")
	if err != nil || deletedReplay.ID != first.ID { t.Fatalf("deleted-worker replay: %+v %v",deletedReplay,err) }
	workers,err := db.ListWorkers(ctx)
	if err != nil { t.Fatal(err) }
	if len(workers)!=1 { t.Fatalf("duplicate scale worker records: %+v",workers) }
}

func TestScaleAdmissionIdentityRejectsMissingGitHubRunnerID(t *testing.T) {
	worker:=core.Worker{PoolID:"pool",RunnerName:"runner",GitHubRunnerID:123,Repository:"acme/app"}
	if sameScaleWorkerIdentity(worker,"pool","runner",0,"acme/app","") {
		t.Fatal("name-only retry should not authenticate a JIT runner identity")
	}
	worker.WorkflowRef="refs/heads/main/.github/workflows/ci.yml"
	if !sameScaleWorkerIdentity(worker,"pool","runner",123,"acme/app","") {
		t.Fatal("successful job metadata enrichment should not break retry identity")
	}
	if sameScaleWorkerIdentity(worker,"other","runner",123,"acme/app","") ||
		sameScaleWorkerIdentity(worker,"pool","runner",123,"attacker/app","") {
		t.Fatal("cross-pool or cross-repository identity was accepted")
	}
}
