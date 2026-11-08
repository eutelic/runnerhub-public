package agentupdate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validTarget() Target {
	return Target{Image: AgentImage + "@sha256:" + strings.Repeat("a", 64), Revision: strings.Repeat("b", 40)}
}

type fakeAdapter struct {
	fail  string
	calls []string
	old   Snapshot
	stage Stage
}

func (f *fakeAdapter) call(name string) error {
	f.calls = append(f.calls, name)
	if f.fail == name {
		return errors.New("simulated " + name + " failure")
	}
	return nil
}
func (f *fakeAdapter) Capture(_ context.Context, id string) (Snapshot, error) {
	if e := f.call("capture"); e != nil {
		return Snapshot{}, e
	}
	if f.old.HostID != "" {
		return f.old, nil
	}
	return Snapshot{HostID: id, ContainerID: "old-container", Image: "old", Running: true, SafeTopology: true}, nil
}
func (f *fakeAdapter) PullAndVerify(context.Context, Target) error { return f.call("pull") }
func (f *fakeAdapter) StageReplacement(context.Context, Snapshot, Target) (Stage, error) {
	if e := f.call("stage"); e != nil {
		return Stage{}, e
	}
	if f.stage.ContainerID != "" {
		return f.stage, nil
	}
	return Stage{ContainerID: "new-container"}, nil
}
func (f *fakeAdapter) Discard(context.Context, Snapshot, Stage) error { return f.call("discard") }
func (f *fakeAdapter) Replace(context.Context, Snapshot, Stage) error { return f.call("replace") }
func (f *fakeAdapter) VerifyRunning(context.Context, Stage, Target) error {
	return f.call("verify-running")
}
func (f *fakeAdapter) WaitForHeartbeat(context.Context, string, string, time.Time) error {
	return f.call("heartbeat")
}
func (f *fakeAdapter) Restore(context.Context, Snapshot, Stage) error  { return f.call("restore") }
func (f *fakeAdapter) Finalize(context.Context, Snapshot, Stage) error { return f.call("finalize") }

type fakeGate struct{ calls, failAt int }

func (g *fakeGate) CheckDrained(context.Context, string) error {
	g.calls++
	if g.failAt == g.calls {
		return errors.New("busy")
	}
	return nil
}

func TestTargetPinnedOfficialDigestAndRevision(t *testing.T) {
	good := validTarget()
	if e := good.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []Target{
		{Image: AgentImage + ":latest", Revision: good.Revision},
		{Image: "ghcr.io/other/runnerhub-agent@sha256:" + strings.Repeat("a", 64), Revision: good.Revision},
		{Image: good.Image, Revision: "abc"},
		{Image: AgentImage + "@sha256:" + strings.Repeat("A", 64), Revision: good.Revision},
	} {
		if bad.Validate() == nil {
			t.Fatalf("accepted unsafe target %+v", bad)
		}
	}
}
func TestTransactionSuccess(t *testing.T) {
	f := &fakeAdapter{}
	g := &fakeGate{}
	r := Run(context.Background(), Request{HostID: "host-one", Target: validTarget()}, f, g, nil)
	if r.State != "succeeded" || r.Err != nil || g.calls != 2 {
		t.Fatalf("unexpected %+v %+v", r, g)
	}
	want := []string{"capture", "pull", "stage", "replace", "verify-running", "heartbeat", "finalize"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls=%v want=%v", f.calls, want)
	}
}
func TestTransactionNeverSwapsBeforeEligibility(t *testing.T) {
	for _, tt := range []struct {
		name, fail string
		busyAt     int
		old        Snapshot
		stage      Stage
	}{
		{name: "busy", busyAt: 1},
		{name: "capture-failure", fail: "capture"},
		{name: "wrong-host", old: Snapshot{HostID: "different", ContainerID: "old", Running: true, SafeTopology: true}},
		{name: "unsupported-topology", old: Snapshot{HostID: "host-one", ContainerID: "old", Running: true}},
		{name: "pull-failure", fail: "pull"},
		{name: "stage-failure", fail: "stage"},
		{name: "same-container", stage: Stage{ContainerID: "old-container"}},
		{name: "race", busyAt: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAdapter{fail: tt.fail, old: tt.old, stage: tt.stage}
			g := &fakeGate{failAt: tt.busyAt}
			r := Run(context.Background(), Request{HostID: "host-one", Target: validTarget()}, f, g, nil)
			if r.State != "failed" || r.Err == nil {
				t.Fatalf("unexpected %+v", r)
			}
			for _, call := range f.calls {
				if call == "replace" || call == "restore" {
					t.Fatalf("destructive action %s: %v", call, f.calls)
				}
			}
		})
	}
}
func TestReplaceFailureRestoresOriginal(t *testing.T) {
	for _, stage := range []string{"replace", "verify-running", "heartbeat"} {
		f := &fakeAdapter{fail: stage}
		r := Run(context.Background(), Request{HostID: "host-one", Target: validTarget()}, f, &fakeGate{}, nil)
		if r.State != "failed" || r.Step != "rolled-back" || f.calls[len(f.calls)-1] != "restore" {
			t.Fatalf("%s did not restore: %+v %v", stage, r, f.calls)
		}
	}
}

type rollbackFails struct{ *fakeAdapter }

func (f rollbackFails) Restore(ctx context.Context, s Snapshot, t Stage) error {
	_ = f.fakeAdapter.Restore(ctx, s, t)
	return errors.New("rollback failed")
}
func TestFailedRollbackCannotReportSuccess(t *testing.T) {
	f := rollbackFails{&fakeAdapter{fail: "replace"}}
	r := Run(context.Background(), Request{HostID: "host-one", Target: validTarget()}, f, &fakeGate{}, nil)
	if r.State != "review" || r.Step != "rollback-unverified" || !errors.Is(r.Err, ErrReview) {
		t.Fatalf("%+v", r)
	}
}
func TestFinalizationFailureIsAmbiguous(t *testing.T) {
	f := &fakeAdapter{fail: "finalize"}
	r := Run(context.Background(), Request{HostID: "host-one", Target: validTarget()}, f, &fakeGate{}, nil)
	if r.State != "review" || r.Step != "finalization-unverified" {
		t.Fatalf("%+v", r)
	}
}
func TestChangedDrainDiscardsStage(t *testing.T) {
	f := &fakeAdapter{}
	r := Run(context.Background(), Request{HostID: "host-one", Target: validTarget()}, f, &fakeGate{failAt: 2}, nil)
	if r.State != "failed" || r.Step != "drain-changed" || f.calls[len(f.calls)-1] != "discard" {
		t.Fatalf("%+v %v", r, f.calls)
	}
}

type discardFails struct{ *fakeAdapter }

func (f discardFails) Discard(ctx context.Context, s Snapshot, st Stage) error {
	_ = f.fakeAdapter.Discard(ctx, s, st)
	return errors.New("cleanup failed")
}
func TestFailedDiscardRequiresReview(t *testing.T) {
	f := discardFails{&fakeAdapter{}}
	r := Run(context.Background(), Request{HostID: "host-one", Target: validTarget()}, f, &fakeGate{failAt: 2}, nil)
	if r.State != "review" || r.Step != "discard-staged-unverified" || !errors.Is(r.Err, ErrReview) {
		t.Fatalf("%+v", r)
	}
}
