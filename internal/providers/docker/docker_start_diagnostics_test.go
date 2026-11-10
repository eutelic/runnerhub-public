package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
)

func TestTrustedWorkerStartErrorIncludesWorkerContext(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "", errors.New("registry unavailable")
		}
		if len(args) > 0 && args[0] == "inspect" {
			return "", errors.New("No such object")
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-start", PoolID: "pool", RunnerName: "runner",
		JITConfig: "secret", Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if err == nil {
		t.Fatal("expected Docker startup error")
	}
	for _, expected := range []string{"start trusted Docker worker", "runnerhub-w-start", "registry unavailable"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error %q missing %q", err, expected)
		}
	}
}

func TestTrustedWorkerStartFailurePreservesCauseAndRemovesJITSecret(t *testing.T) {
	cause := errors.New("registry temporarily unreachable")
	stateDir := t.TempDir()
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "", cause
		}
		if len(args) > 0 && args[0] == "inspect" {
			return "", errors.New("No such object")
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-failed", PoolID: "pool", RunnerName: "runner",
		JITConfig: "sensitive-jit-token", Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("startup error must wrap original cause: %v", err)
	}
	if strings.Contains(err.Error(), "sensitive-jit-token") {
		t.Fatalf("startup error exposed JIT secret: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "workers", "w-failed", "jitconfig")); !os.IsNotExist(statErr) {
		t.Fatalf("failed startup must remove JIT secret; stat error: %v", statErr)
	}
}

func TestTrustedWorkerStartFailureDoesNotReportJITConfigInContext(t *testing.T) {
	secret := "super-sensitive-jit-config"
	cause := errors.New("daemon rejected container start")
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "", cause
		}
		if len(args) > 0 && args[0] == "inspect" {
			return "", errors.New("No such object")
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-secret", PoolID: "pool", RunnerName: "runner",
		JITConfig: secret, Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("expected original daemon error, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("startup diagnostics leaked JIT configuration")
	}
}

func TestTrustedWorkerStartFailureWithInspectErrorStillCleansSecret(t *testing.T) {
	cause := errors.New("daemon unavailable")
	stateDir := t.TempDir()
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "", cause
		}
		if len(args) > 0 && args[0] == "inspect" {
			return "", errors.New("inspection also unavailable")
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-inspect-error", PoolID: "pool", RunnerName: "runner",
		JITConfig: "inspect-failure-secret", Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("must preserve original startup error despite inspection failure: %v", err)
	}
	if strings.Contains(err.Error(), "inspect-failure-secret") {
		t.Fatalf("startup failure leaked JIT configuration: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "workers", "w-inspect-error", "jitconfig")); !os.IsNotExist(statErr) {
		t.Fatalf("inspection failure must not leave JIT secret on disk: %v", statErr)
	}
}

func TestTrustedWorkerStartErrorRedactsSecretEchoedByDaemon(t *testing.T) {
	secret := "private-jit-token"
	cause := errors.New("daemon rejected config " + secret)
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "", cause
		}
		if len(args) > 0 && args[0] == "inspect" {
			return "", errors.New("No such object")
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: t.TempDir(), AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-echo", PoolID: "pool", RunnerName: "runner",
		JITConfig: secret, Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("must retain original daemon error identity: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("daemon echo exposed JIT configuration: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected redaction marker: %v", err)
	}
}

func TestTrustedWorkerStartErrorSanitizesAdditionalDaemonCredentials(t *testing.T) {
	cause := errors.New("registry denied authorization: Bearer secondary-private-token")
	err := &trustedWorkerStartError{
		name:      "runnerhub-w-safe",
		cause:     cause,
		jitConfig: "unrelated-jit-secret",
	}
	if !errors.Is(err, cause) {
		t.Fatalf("must retain original daemon error: %v", err)
	}
	if strings.Contains(err.Error(), "secondary-private-token") {
		t.Fatalf("daemon diagnostics exposed an unrelated credential: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected credential redaction: %v", err)
	}
	if !strings.Contains(err.Error(), "runnerhub-w-safe") {
		t.Fatalf("missing worker context: %v", err)
	}
}

func TestTrustedWorkerStartFailureRedactsJITAndSecondaryCredentialTogether(t *testing.T) {
	jit := "private-jit-payload"
	bearer := "secondary-bearer-credential"
	cause := errors.New("daemon rejected " + jit + ": authorization: Bearer " + bearer)
	stateDir := t.TempDir()
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "", cause
		}
		if len(args) > 0 && args[0] == "inspect" {
			return "", errors.New("No such object")
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-two-secrets", PoolID: "pool", RunnerName: "runner",
		JITConfig: jit, Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("startup failure must preserve original cause: %v", err)
	}
	for _, secret := range []string{jit, bearer} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("startup diagnostics exposed a credential")
		}
	}
	if !strings.Contains(err.Error(), "runnerhub-w-two-secrets") {
		t.Fatalf("startup diagnostics lost worker context: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "workers", "w-two-secrets", "jitconfig")); !os.IsNotExist(statErr) {
		t.Fatalf("failed startup left JIT secret on disk: %v", statErr)
	}
}

func TestTrustedWorkerStartErrorRedactsRepeatedJITInWrappedDaemonError(t *testing.T) {
	jit := "repeated-private-jit-payload"
	root := errors.New("registry rejected payload")
	cause := fmt.Errorf("daemon echoed %s twice (%s): %w", jit, jit, root)
	err := &trustedWorkerStartError{
		name:      "runnerhub-w-repeat",
		cause:     cause,
		jitConfig: jit,
	}
	if !errors.Is(err, root) {
		t.Fatalf("wrapped daemon cause was lost: %v", err)
	}
	var typed *trustedWorkerStartError
	if !errors.As(err, &typed) || typed != err {
		t.Fatalf("typed startup failure unavailable through errors.As: %v", err)
	}
	message := err.Error()
	if strings.Contains(message, jit) {
		t.Fatal("repeated JIT secret leaked in startup diagnostics")
	}
	if strings.Count(message, "[REDACTED]") != 2 {
		t.Fatalf("expected both secret echoes to be redacted: %q", message)
	}
	if !strings.Contains(message, "runnerhub-w-repeat") {
		t.Fatalf("worker identity missing from startup diagnostics: %q", message)
	}
}

func TestTrustedWorkerStartErrorRedactsJSONEscapedJIT(t *testing.T) {
	jit := "private\\quoted\"payload"
	encoded, err := json.Marshal(jit)
	if err != nil {
		t.Fatal(err)
	}
	root := errors.New("daemon failed")
	cause := fmt.Errorf("daemon returned %s: %w", string(encoded), root)
	startErr := &trustedWorkerStartError{name: "runnerhub-w-json", cause: cause, jitConfig: jit}
	if !errors.Is(startErr, root) {
		t.Fatalf("original error identity lost: %v", startErr)
	}
	message := startErr.Error()
	if strings.Contains(message, string(encoded)) || strings.Contains(message, "private") {
		t.Fatalf("JSON-escaped JIT leaked in diagnostics: %q", message)
	}
	if !strings.Contains(message, "[REDACTED]") {
		t.Fatalf("expected JIT redaction marker: %q", message)
	}
}

func TestTrustedWorkerResolveFailureRedactsDaemonSecrets(t *testing.T) {
	jit := "resolve-secret-jit-payload"
	cause := errors.New("inspect daemon echoed " + jit)
	stateDir := t.TempDir()
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "\n\n", nil
		}
		if len(args) > 0 && args[0] == "inspect" {
			return "", cause
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-resolve", PoolID: "pool", RunnerName: "runner",
		JITConfig: jit, Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("container ID resolution must preserve daemon cause: %v", err)
	}
	if strings.Contains(err.Error(), jit) {
		t.Fatalf("resolution failure leaked JIT config: %v", err)
	}
	if !strings.Contains(err.Error(), "runnerhub-w-resolve") {
		t.Fatalf("resolution failure omitted worker identity: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "workers", "w-resolve", "jitconfig")); !os.IsNotExist(statErr) {
		t.Fatalf("resolution failure left JIT secret on disk: %v", statErr)
	}
}

func TestTrustedWorkerSingleLineDaemonOutputUsesExactInspectID(t *testing.T) {
	const containerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	stateDir := t.TempDir()
	inspected := false
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "warning: daemon emitted diagnostic instead of container ID\n", nil
		}
		if len(args) > 0 && args[0] == "inspect" {
			inspected = true
			return containerID + "\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-output", PoolID: "pool", RunnerName: "runner",
		JITConfig: "secret", Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inspected {
		t.Fatal("single-line non-ID output must trigger exact container inspection")
	}
	if handle.ProviderID != containerID {
		t.Fatalf("provider ID = %q; want inspected ID %q", handle.ProviderID, containerID)
	}
}

func TestTrustedWorkerRejectsInvalidInspectedContainerID(t *testing.T) {
	stateDir := t.TempDir()
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "warning: no container id returned\n", nil
		}
		if len(args) > 0 && args[0] == "inspect" {
			return "warning: inspect returned diagnostic, not an id\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-invalid-inspect", PoolID: "pool", RunnerName: "runner",
		JITConfig: "secret", Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if err == nil || !strings.Contains(err.Error(), "inspect returned no valid container id") {
		t.Fatalf("expected invalid inspected id rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "workers", "w-invalid-inspect", "jitconfig")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid inspected id left JIT secret on disk: %v", statErr)
	}
}

func TestTrustedWorkerInvalidInspectedIDRemovesExactContainerWithVolumes(t *testing.T) {
	stateDir := t.TempDir()
	const workerID = "w-invalid-cleanup"
	const name = "runnerhub-" + workerID
	var removed bool
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) == 0 {
			return "", nil
		}
		switch args[0] {
		case "run":
			return "warning: no detached ID\n", nil
		case "inspect":
			return "not-a-container-id\n", nil
		case "rm":
			if len(args) != 4 || args[1] != "-f" || args[2] != "-v" || args[3] != name {
				t.Fatalf("cleanup must remove exact worker and anonymous volumes, got %v", args)
			}
			removed = true
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: workerID, PoolID: "pool", RunnerName: "runner",
		JITConfig: "cleanup-jit-secret", Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if err == nil {
		t.Fatal("expected invalid container ID to fail startup")
	}
	if !removed {
		t.Fatal("invalid inspected ID must trigger scoped container and volume cleanup")
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "workers", workerID, "jitconfig")); !os.IsNotExist(statErr) {
		t.Fatalf("failed worker retained JIT secret: %v", statErr)
	}
}

func TestTrustedWorkerRunFailureRejectsInvalidRecoveredContainerID(t *testing.T) {
	cause := errors.New("daemon failed after container creation")
	stateDir := t.TempDir()
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "", cause
		}
		if len(args) > 0 && args[0] == "inspect" {
			return "warning: inspect returned a diagnostic, not an ID\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-invalid-recovery", PoolID: "pool", RunnerName: "runner",
		JITConfig: "private-recovery-secret", Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("invalid recovered ID must not mask original startup error: %v", err)
	}
	if strings.Contains(err.Error(), "private-recovery-secret") {
		t.Fatalf("startup error exposed JIT secret: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "workers", "w-invalid-recovery", "jitconfig")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid recovered ID left JIT secret on disk: %v", statErr)
	}
}

func TestTrustedWorkerRunFailureRecoversExactValidContainerID(t *testing.T) {
	const containerID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cause := errors.New("daemon lost response after creating container")
	stateDir := t.TempDir()
	var inspectedName string
	f := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "run" {
			return "", cause
		}
		if len(args) > 0 && args[0] == "inspect" {
			inspectedName = args[1]
			return containerID + "\n", nil
		}
		return "", nil
	}}
	p, err := newWithRunner(Config{StateDir: stateDir, AllowedImages: []string{"runner:test"}}, f)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := p.Create(context.Background(), provider.WorkerSpec{
		WorkerID: "w-recovered", PoolID: "pool", RunnerName: "runner",
		JITConfig: "recovery-jit-secret", Image: "runner:test", Isolation: core.IsolationContainerTrusted,
	})
	if err != nil {
		t.Fatalf("valid exact container recovery must succeed: %v", err)
	}
	if inspectedName != "runnerhub-w-recovered" {
		t.Fatalf("inspected %q; expected exact worker container name", inspectedName)
	}
	if handle.ProviderID != containerID {
		t.Fatalf("provider ID = %q; want %q", handle.ProviderID, containerID)
	}
	idBytes, err := os.ReadFile(filepath.Join(stateDir, "workers", "w-recovered", "provider-id"))
	if err != nil || string(idBytes) != containerID {
		t.Fatalf("persisted provider ID = %q, error = %v", idBytes, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "workers", "w-recovered", "jitconfig")); err != nil {
		t.Fatalf("successful recovery must retain JIT config for worker startup: %v", err)
	}
}
