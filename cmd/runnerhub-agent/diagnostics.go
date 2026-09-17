package main

import (
	"context"
	"os/exec"
	"regexp"
	"strings"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
)

const maxSupportDiagnosticsBytes = 16 << 10

type diagnosticProvider struct {
	provider.Provider
	dockerBinary string
}

func newDiagnosticProvider(inner provider.Provider, dockerBinary string) provider.Provider {
	if strings.TrimSpace(dockerBinary) == "" {
		dockerBinary = "docker"
	}
	return &diagnosticProvider{Provider: inner, dockerBinary: dockerBinary}
}

func (p *diagnosticProvider) Inspect(ctx context.Context, handle provider.WorkerHandle) (provider.WorkerState, error) {
	state, err := p.Provider.Inspect(ctx, handle)
	if err != nil || strings.TrimSpace(state.Diagnostics) != "" {
		return state, err
	}

	// The Docker provider historically treats exit code 0 as a normal completion.
	// When that happens before GitHub reports the worker runnable, the control plane
	// correctly converts it to a pre-job failure but would otherwise have no startup
	// evidence. Capture only bounded, redacted container logs here. The control plane
	// persists Diagnostics only while the worker is still pre-job, so normal job
	// output is not retained in the RunnerHub database.
	if state.State == core.WorkerCompleted && state.ExitCode != nil && strings.TrimSpace(handle.ProviderID) != "" {
		cmd := exec.CommandContext(ctx, p.dockerBinary, "logs", "--tail", "200", handle.ProviderID)
		out, _ := cmd.CombinedOutput()
		state.Diagnostics = sanitizeSupportDiagnostics(string(out))
	}
	return state, err
}

var supportDiagnosticSecretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)(authorization\s*:\s*(?:bearer|token|basic)\s+)\S+`), "$1[REDACTED]"},
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`), "$1[REDACTED]"},
	{regexp.MustCompile(`(?i)(github_pat_|gh[pousr]_)[A-Za-z0-9_]{12,}`), "$1[REDACTED]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\b`), "[REDACTED-JWT]"},
	{regexp.MustCompile(`(?i)((?:token|secret|password|jitconfig)\s*[=:]\s*)\S+`), "$1[REDACTED]"},
	{regexp.MustCompile(`[A-Za-z0-9_-]{96,}`), "[REDACTED-LONG-TOKEN]"},
}

func sanitizeSupportDiagnostics(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	lines := strings.Split(value, "\n")
	safe := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "running job:") ||
			strings.Contains(lower, "runner.worker") ||
			strings.Contains(lower, "job execution started") {
			safe = append(safe, "[job output omitted]")
			break
		}
		safe = append(safe, line)
	}
	value = strings.Join(safe, "\n")
	for _, pattern := range supportDiagnosticSecretPatterns {
		value = pattern.re.ReplaceAllString(value, pattern.repl)
	}
	value = strings.TrimSpace(value)
	if len(value) > maxSupportDiagnosticsBytes {
		value = "[truncated]\n" + value[len(value)-maxSupportDiagnosticsBytes:]
	}
	return value
}
