//go:build !windows

package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
)

func TestNativeRunnerCompletionIsReapedAndInspectable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		state  core.WorkerState
		exit   int
	}{
		{"success", "#!/bin/sh\nexit 0\n", core.WorkerCompleted, 0},
		{"failure", "#!/bin/sh\nexit 17\n", core.WorkerFailed, 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runnerDir := t.TempDir()
			stateDir := t.TempDir()
			script := filepath.Join(runnerDir, "run.sh")
			if err := os.WriteFile(script, []byte(tc.script), 0700); err != nil {
				t.Fatal(err)
			}
			p, err := New(Config{RunnerDir: runnerDir, StateDir: stateDir, HostID: "test"})
			if err != nil {
				t.Fatal(err)
			}
			h, err := p.Create(context.Background(), provider.WorkerSpec{
				WorkerID: "native", PoolID: "test", RunnerName: "test", JITConfig: "dummy-config",
				Labels: []string{"runnerhub"}, Isolation: core.IsolationHostPersistent,
			})
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				got, err := p.Inspect(context.Background(), h)
				if err != nil {
					t.Fatal(err)
				}
				if got.State == tc.state {
					if got.ExitCode == nil || *got.ExitCode != tc.exit {
						t.Fatalf("exit code got %+v, wanted %d", got.ExitCode, tc.exit)
					}
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("native runner never became terminal: child exit was not reaped")
		})
	}
}

func TestNativeRunnerCanRetryAfterPreviousFailedProcess(t *testing.T) {
	runnerDir := t.TempDir()
	stateDir := t.TempDir()
	script := filepath.Join(runnerDir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 12\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{RunnerDir: runnerDir, StateDir: stateDir, HostID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	spec := provider.WorkerSpec{
		WorkerID: "retry", PoolID: "test", RunnerName: "test", JITConfig: "dummy",
		Labels: []string{"runnerhub"}, Isolation: core.IsolationHostPersistent,
	}
	h, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	wait := func(want core.WorkerState) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			s, err := p.Inspect(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			if s.State == want {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("native runner never entered %s", want)
	}
	wait(core.WorkerFailed)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	wait(core.WorkerCompleted)
}

func TestNativeUnknownExitDoesNotReportSuccess(t *testing.T) {
	stateDir := t.TempDir()
	p, err := New(Config{RunnerDir: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(stateDir, "missing-runner")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte("999999999"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := p.Inspect(context.Background(), provider.WorkerHandle{ProviderID: "missing-runner"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.WorkerFailed {
		t.Fatalf("unconfirmed runner exit must not pass; got %+v", got)
	}
}

func TestNativeExitFileIsWrittenAtomically(t *testing.T) {
	dir := t.TempDir()
	if err := writeNativeExitCode(dir, 17); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "exit-code"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "17" {
		t.Fatalf("exit code=%q, want 17", b)
	}
}

func TestNativeRunnerExitedBeforeExitCodePersistDoesNotReportUnknownFailure(t *testing.T) {
	runnerDir := t.TempDir()
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runnerDir, "run.sh"), []byte("#!/bin/sh\nexit 17\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{RunnerDir: runnerDir, StateDir: stateDir, HostID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	resume := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})
	p.persistExitCode = func(dir string, code int) error {
		close(reaped) // cmd.Wait has finished; no exit-code file exists yet
		<-resume
		return writeNativeExitCode(dir, code)
	}
	h, err := p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "delayed-exit", PoolID: "test", RunnerName: "test",
		JITConfig: "dummy-config", Labels: []string{"runnerhub"},
		Isolation: core.IsolationHostPersistent,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("native runner did not exit")
	}
	pending, err := p.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != core.WorkerReady || pending.ExitCode != nil {
		t.Fatalf("must not declare failure before reaped exit code is persisted: %+v", pending)
	}
	close(resume)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := p.Inspect(context.Background(), h)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == core.WorkerFailed && got.ExitCode != nil && *got.ExitCode == 17 {
			return
		}
		if got.State == core.WorkerFailed && got.ExitCode == nil {
			t.Fatalf("lost exit code during persistence: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runner did not report persisted failure exit code")
}
