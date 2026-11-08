//go:build !localtest

package store

import (
	"context"
	"errors"
	"github.com/eutelic/runnerhub/internal/core"
	"testing"
	"time"
)

func readyUpdateLease(t *testing.T) (*SQLite, AgentUpdateLease) {
	t.Helper()
	ctx := context.Background()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	h := core.Host{ID: "host-test", Provider: "docker", State: core.HostOnline, AgentVersion: "old"}
	if err := st.UpsertHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	l, _, err := st.BeginAgentUpdateLease(ctx, leaseTarget(h.ID, "op-test"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.TransitionAgentUpdateLease(ctx, h.ID, l.OperationID, "draining", "replacing", "helper-launched"); err != nil {
		t.Fatal(err)
	}
	return st, l
}
func TestAgentUpdateSuccessNeedsExactNewHeartbeatBeforeResuming(t *testing.T) {
	st, l := readyUpdateLease(t)
	defer st.Close()
	ctx := context.Background()
	if _, err := st.CompleteAgentUpdateLease(ctx, l.HostID, l.OperationID, "succeeded"); !errors.Is(err, ErrAgentUpdateChanged) {
		t.Fatalf("old revision accepted: %v", err)
	}
	h, _ := st.GetHost(ctx, l.HostID)
	if h.State != core.HostDraining {
		t.Fatal("host prematurely resumed")
	}
	h.AgentVersion = l.TargetRevision
	h.LastSeenAt = time.Now().UTC()
	if err := st.UpsertHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	completed, err := st.CompleteAgentUpdateLease(ctx, l.HostID, l.OperationID, "succeeded")
	if err != nil || completed.State != "succeeded" {
		t.Fatalf("%+v %v", completed, err)
	}
	got, _ := st.GetHost(ctx, l.HostID)
	if got.State != core.HostOnline {
		t.Fatalf("host not resumed: %+v", got)
	}
	same, err := st.CompleteAgentUpdateLease(ctx, l.HostID, l.OperationID, "succeeded")
	if err != nil || same.State != "succeeded" {
		t.Fatalf("idempotent completion failed: %+v %v", same, err)
	}
}
func TestAgentUpdateCannotResumeWithPendingCleanup(t *testing.T) {
	st, l := readyUpdateLease(t)
	defer st.Close()
	ctx := context.Background()
	h, _ := st.GetHost(ctx, l.HostID)
	h.AgentVersion = l.TargetRevision
	h.LastSeenAt = time.Now().UTC()
	if err := st.UpsertHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorker(ctx, core.Worker{ID: "cleanup", HostID: l.HostID, State: core.WorkerCleanupPending, ProviderHandle: "docker-old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteAgentUpdateLease(ctx, l.HostID, l.OperationID, "succeeded"); !errors.Is(err, ErrAgentUpdateChanged) {
		t.Fatalf("pending cleanup allowed: %v", err)
	}
	h, _ = st.GetHost(ctx, l.HostID)
	if h.State != core.HostDraining {
		t.Fatal("host resumed despite unfinished cleanup")
	}
}
func TestAgentUpdateReviewLeavesAdmissionBlocked(t *testing.T) {
	st, l := readyUpdateLease(t)
	defer st.Close()
	ctx := context.Background()
	completed, err := st.CompleteAgentUpdateLease(ctx, l.HostID, l.OperationID, "review")
	if err != nil || completed.State != "review" {
		t.Fatalf("%+v %v", completed, err)
	}
	h, _ := st.GetHost(ctx, l.HostID)
	if h.State != core.HostDraining {
		t.Fatalf("ambiguous update resumed host: %+v", h)
	}
	if _, err := st.CompleteAgentUpdateLease(ctx, l.HostID, l.OperationID, "succeeded"); !errors.Is(err, ErrAgentUpdateChanged) {
		t.Fatalf("review state bypassed: %v", err)
	}
}

func TestRecoveryLeaseFencesLateSuccessAndKeepsHostDrained(t *testing.T) {
	st, l := readyUpdateLease(t)
	defer st.Close()
	ctx := context.Background()
	recoveryOwner := "0123456789abcdef0123456789abcdef"
	if _, err := st.ClaimAgentUpdateRecovery(ctx, l.HostID, l.OperationID, recoveryOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAgentUpdateRecovery(ctx, l.HostID, l.OperationID, recoveryOwner); err != nil {
		t.Fatalf("same recovery helper must be idempotent: %v", err)
	}
	if _, err := st.ClaimAgentUpdateRecovery(ctx, l.HostID, l.OperationID, "fedcba9876543210fedcba9876543210"); !errors.Is(err, ErrAgentUpdateChanged) {
		t.Fatalf("second recovery helper shared exclusive lease: %v", err)
	}
	h, _ := st.GetHost(ctx, l.HostID)
	h.AgentVersion = l.TargetRevision
	h.LastSeenAt = time.Now().UTC()
	if err := st.UpsertHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"succeeded", "review"} {
		if _, err := st.CompleteAgentUpdateLease(ctx, l.HostID, l.OperationID, outcome); !errors.Is(err, ErrAgentUpdateChanged) {
			t.Fatalf("recovery did not fence stale %s report: %v", outcome, err)
		}
	}
	h, _ = st.GetHost(ctx, l.HostID)
	if h.State != core.HostDraining {
		t.Fatal("stale updater resumed host during recovery")
	}
	if _, err := st.CompleteAgentUpdateLease(ctx, l.HostID, l.OperationID, "rolled-back"); !errors.Is(err, ErrAgentUpdateChanged) {
		t.Fatalf("ownerless rollback claim was accepted: %v", err)
	}
	if _, err := st.CompleteAgentUpdateLease(ctx, l.HostID, l.OperationID, "rolled-back", "fedcba9876543210fedcba9876543210"); !errors.Is(err, ErrAgentUpdateChanged) {
		t.Fatalf("other recovery helper completed lease: %v", err)
	}
	got, err := st.CompleteAgentUpdateLease(ctx, l.HostID, l.OperationID, "rolled-back", recoveryOwner)
	if err != nil || got.State != "rolled-back" {
		t.Fatalf("recovered rollback not accepted: %+v %v", got, err)
	}
	h, _ = st.GetHost(ctx, l.HostID)
	if h.State != core.HostDraining {
		t.Fatal("rolled-back host was admitted without operator review")
	}
}

func TestRecoveryClaimRequiresHostStillDrainedAndFreeOfJobs(t *testing.T) {
	st, l := readyUpdateLease(t)
	defer st.Close()
	ctx := context.Background()
	owner := "0123456789abcdef0123456789abcdef"
	h, err := st.GetHost(ctx, l.HostID)
	if err != nil {
		t.Fatal(err)
	}
	h.State = core.HostOnline
	if err := st.UpsertHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAgentUpdateRecovery(ctx, l.HostID, l.OperationID, owner); !errors.Is(err, ErrAgentUpdateChanged) {
		t.Fatalf("host re-admitted to CI was eligible for rollback: %v", err)
	}
	h.State = core.HostDraining
	if err := st.UpsertHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorker(ctx, core.Worker{ID: "active-work", HostID: l.HostID, State: core.WorkerBusy}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAgentUpdateRecovery(ctx, l.HostID, l.OperationID, owner); !errors.Is(err, ErrAgentUpdateChanged) {
		t.Fatalf("host with unfinished CI jobs was eligible for rollback: %v", err)
	}
	if err := st.UpsertWorker(ctx, core.Worker{ID: "active-work", HostID: l.HostID, State: core.WorkerCleanupPending, ProviderHandle: "docker-id"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAgentUpdateRecovery(ctx, l.HostID, l.OperationID, owner); !errors.Is(err, ErrAgentUpdateChanged) {
		t.Fatalf("pending Docker cleanup was eligible for rollback: %v", err)
	}
	if err := st.UpsertWorker(ctx, core.Worker{ID: "active-work", HostID: l.HostID, State: core.WorkerDeleted}); err != nil {
		t.Fatal(err)
	}
	claim, err := st.ClaimAgentUpdateRecovery(ctx, l.HostID, l.OperationID, owner)
	if err != nil || claim.State != "recovering" {
		t.Fatalf("drained idle host could not recover after worker cleanup: %+v %v", claim, err)
	}
}
