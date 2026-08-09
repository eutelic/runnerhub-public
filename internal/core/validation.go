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
	if !validIsolation(pool.Isolation) {
		return fmt.Errorf("unsupported isolation mode %q", pool.Isolation)
	}
	if pool.Resources.CPUMillis < 0 || pool.Resources.MemoryBytes < 0 || pool.Resources.DiskBytes < 0 || pool.Resources.GPUs < 0 {
		return errors.New("resource requests cannot be negative")
	}
	if err := validateLabels(pool.Labels); err != nil {
		return err
	}
	return nil
}

func validIsolation(mode IsolationMode) bool {
	switch mode {
	case IsolationContainerTrusted, IsolationHostPersistent, IsolationMicroVM, IsolationVMProvider:
		return true
	default:
		return false
	}
}

func validateLabels(labels []string) error {
	if len(labels) == 0 {
		return errors.New("at least one runner label is required")
	}
	if len(labels) > 100 {
		return errors.New("runner labels cannot exceed GitHub's 100-label JIT limit")
	}
	seen := make(map[string]struct{}, len(labels))
	for _, raw := range labels {
		label := strings.TrimSpace(raw)
		if label == "" {
			return errors.New("runner labels cannot be empty")
		}
		key := strings.ToLower(label)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate runner label %q", label)
		}
		seen[key] = struct{}{}
	}
	return nil
}
