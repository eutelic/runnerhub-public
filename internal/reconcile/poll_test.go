package reconcile

import (
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
)

func TestGitHubRunnerPollCadence(t *testing.T) {
	r := &Reconciler{}
	now := time.Unix(1000, 0)
	cases := []struct {
		state core.WorkerState
		wait  time.Duration
	}{
		{state: core.WorkerRegistering, wait: 15 * time.Second},
		{state: core.WorkerReady, wait: 2 * time.Minute},
		{state: core.WorkerBusy, wait: 5 * time.Minute},
	}
	for i, tc := range cases {
		w := core.Worker{ID: string(rune('a' + i)), State: tc.state}
		if !r.githubRunnerPollDue(w, now) {
			t.Fatalf("%s initial poll was suppressed", tc.state)
		}
		if r.githubRunnerPollDue(w, now.Add(tc.wait-time.Second)) {
			t.Fatalf("%s polled before %s elapsed", tc.state, tc.wait)
		}
		if !r.githubRunnerPollDue(w, now.Add(tc.wait)) {
			t.Fatalf("%s did not poll after %s elapsed", tc.state, tc.wait)
		}
	}
}

func TestTerminalWorkerDoesNotPollGitHubRunner(t *testing.T) {
	r := &Reconciler{}
	if r.githubRunnerPollDue(core.Worker{ID: "done", State: core.WorkerCompleted}, time.Now()) {
		t.Fatal("completed worker was considered pollable")
	}
}
