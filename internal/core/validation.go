package core

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var poolNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func ValidateGitHubScope(scope GitHubScope) error {
	if strings.TrimSpace(scope.Owner) == "" {
		return errors.New("github scope owner is required")
	}
	switch scope.Kind {
	case ScopeRepository:
		if strings.TrimSpace(scope.Repository) == "" {
			return errors.New("repository scope requires a repository")
		}
	case ScopeOrganization:
		if scope.Repository != "" {
			return errors.New("organization scope must not include a repository")
		}
	default:
		return fmt.Errorf("unsupported github scope kind %q", scope.Kind)
	}
	return nil
}
func ValidateRunnerPool(pool RunnerPool) error {
	if !poolNamePattern.MatchString(pool.Name) {
		return errors.New("pool name must be 1-63 lowercase alphanumeric or hyphen characters")
	}
	if err := ValidateGitHubScope(pool.Scope); err != nil {
		return err
	}
	if pool.MaxConcurrency < 1 {
		return errors.New("max concurrency must be at least 1")
	}
	if pool.MinIdle < 0 || pool.MinIdle > pool.MaxConcurrency {
		return errors.New("min idle must be between 0 and max concurrency")
	}
	if pool.RunnerGroupID <= 0 {
		return errors.New("runner group id must be positive")
	}
	if !validIsolation(pool.Isolation) {
		return fmt.Errorf("unsupported isolation mode %q", pool.Isolation)
	}
	if !NonNegative(pool.Resources) {
		return errors.New("resource requests cannot be negative")
	}
	return ValidateLabels(pool.Labels)
}
func validIsolation(m IsolationMode) bool {
	switch m {
	case IsolationContainerTrusted, IsolationHostPersistent, IsolationMicroVM, IsolationVMProvider:
		return true
	}
	return false
}
func ValidateLabels(labels []string) error {
	if len(labels) == 0 {
		return errors.New("at least one runner label is required")
	}
	if len(labels) > 100 {
		return errors.New("runner labels cannot exceed GitHub's 100-label JIT limit")
	}
	seen := map[string]struct{}{}
	for _, raw := range labels {
		l := strings.TrimSpace(raw)
		if l == "" {
			return errors.New("runner labels cannot be empty")
		}
		k := strings.ToLower(l)
		if _, ok := seen[k]; ok {
			return fmt.Errorf("duplicate runner label %q", l)
		}
		seen[k] = struct{}{}
	}
	return nil
}
func NonNegative(r Resources) bool {
	return r.CPUMillis >= 0 && r.MemoryBytes >= 0 && r.DiskBytes >= 0 && r.GPUs >= 0
}
func Fits(cap, used, req Resources) bool {
	return cap.CPUMillis-used.CPUMillis >= req.CPUMillis && cap.MemoryBytes-used.MemoryBytes >= req.MemoryBytes && cap.DiskBytes-used.DiskBytes >= req.DiskBytes && cap.GPUs-used.GPUs >= req.GPUs
}
func Add(a, b Resources) Resources {
	return Resources{CPUMillis: a.CPUMillis + b.CPUMillis, MemoryBytes: a.MemoryBytes + b.MemoryBytes, DiskBytes: a.DiskBytes + b.DiskBytes, GPUs: a.GPUs + b.GPUs}
}
