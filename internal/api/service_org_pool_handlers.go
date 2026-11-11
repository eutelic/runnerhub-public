package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/images"
)

type configureOrganizationPoolRequest struct {
	Organization        string             `json:"organization"`
	InstallationID      int64              `json:"installationId"`
	RunnerGroupID       int64              `json:"runnerGroupId"`
	ScaleSetName        string             `json:"scaleSetName,omitempty"`
	AllowedRepositories []string           `json:"allowedRepositories"`
	AllowedWorkflows    []string           `json:"allowedWorkflows,omitempty"`
	MaxConcurrency      int                `json:"maxConcurrency,omitempty"`
	Isolation           core.IsolationMode `json:"isolation,omitempty"`
	Resources           core.Resources     `json:"resources,omitempty"`
	Enabled             *bool              `json:"enabled,omitempty"`
}

func (s *Service) configureOrganizationPool(w http.ResponseWriter, r *http.Request) {
	s.poolMutationMu.Lock()
	defer s.poolMutationMu.Unlock()
	var in configureOrganizationPoolRequest
	if !decode(w, r, &in) {
		return
	}
	in.Organization = strings.TrimSpace(in.Organization)
	in.ScaleSetName = strings.TrimSpace(in.ScaleSetName)
	if in.Organization == "" {
		problem(w, http.StatusBadRequest, errors.New("organization is required"))
		return
	}
	if in.ScaleSetName == "" {
		in.ScaleSetName = "runnerhub"
	}
	if in.Enabled != nil && !*in.Enabled {
		s.disableOrganizationPool(w, r, in.Organization, in.ScaleSetName)
		return
	}
	if s.GitHubAuth == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("github browser authentication is not configured"))
		return
	}
	if s.Catalog == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("github runner-group catalog is not configured"))
		return
	}
	if in.InstallationID <= 0 || in.RunnerGroupID <= 0 {
		problem(w, http.StatusBadRequest, errors.New("installationId and a selected runnerGroupId are required"))
		return
	}
	if !core.NonNegative(in.Resources) || in.MaxConcurrency < 0 {
		problem(w, http.StatusBadRequest, errors.New("resources and maxConcurrency must be nonnegative"))
		return
	}
	if len(in.AllowedRepositories) == 0 {
		problem(w, http.StatusBadRequest, errors.New("select at least one private repository for the organization pool"))
		return
	}
	allowedWorkflows := trimStringList(in.AllowedWorkflows)
	seenWorkflows := make(map[string]bool, len(allowedWorkflows))
	for _, workflow := range allowedWorkflows {
		key := strings.ToLower(workflow)
		if workflow == "" || seenWorkflows[key] {
			problem(w, http.StatusBadRequest, errors.New("workflow allowlist contains an empty or duplicate workflow"))
			return
		}
		seenWorkflows[key] = true
	}
	if in.MaxConcurrency <= 0 {
		in.MaxConcurrency = 2
	}
	if in.Isolation == "" {
		in.Isolation = core.IsolationContainerTrusted
	}
	if in.Isolation == core.IsolationContainerDinD && !strings.EqualFold(in.ScaleSetName, publisherScaleSetName) {
		problem(w, http.StatusBadRequest, fmt.Errorf("restricted shared publisher pools must use the %q label", publisherScaleSetName))
		return
	}

	allowedRepositories, err := s.validateOrganizationRepositories(r, in.Organization, in.InstallationID, in.AllowedRepositories)
	if err != nil {
		problem(w, http.StatusForbidden, err)
		return
	}
	pool := core.RunnerPool{
		ID: "pool-" + randomID(), Name: runnerPoolName("org", in.Organization, in.ScaleSetName),
		Scope:  core.GitHubScope{Kind: core.ScopeOrganization, Owner: in.Organization},
		Labels: []string{in.ScaleSetName}, Isolation: in.Isolation,
		Resources: normalizeConnectResources(in.Resources, core.Resources{}),
		MinIdle:   0, MaxConcurrency: in.MaxConcurrency, RunnerGroupID: in.RunnerGroupID,
		GitHubInstallationID: in.InstallationID, ScaleSetName: in.ScaleSetName,
		AllowedRepositories: allowedRepositories, AllowedWorkflows: allowedWorkflows,
		Image: images.DefaultRunnerImage,
	}
	if err := core.ValidateRunnerPool(pool); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Policy.ValidatePool(pool); err != nil {
		problem(w, http.StatusForbidden, err)
		return
	}
	posture, err := githubrunner.InspectPoolSecurity(r.Context(), s.Catalog, pool)
	if err != nil {
		problem(w, http.StatusBadGateway, err)
		return
	}
	if !posture.Safe || len(posture.Warnings) > 0 {
		findings := append(append([]string(nil), posture.Reasons...), posture.Warnings...)
		problem(w, http.StatusConflict, fmt.Errorf("organization runner-group preflight failed: %s", strings.Join(findings, "; ")))
		return
	}
	accessible := make(map[string]bool, len(posture.AccessibleRepositories))
	for _, name := range posture.AccessibleRepositories {
		accessible[strings.ToLower(name)] = true
	}
	for _, name := range allowedRepositories {
		if !accessible[strings.ToLower(name)] {
			problem(w, http.StatusConflict, fmt.Errorf("private repository %s is not selected in the GitHub runner group", name))
			return
		}
	}

	pools, err := s.Store.ListPools(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	var previous *core.RunnerPool
	for i := range pools {
		candidate := pools[i]
		if candidate.Scope.Kind == core.ScopeOrganization && strings.EqualFold(candidate.Scope.Owner, in.Organization) && strings.EqualFold(candidate.ScaleSetName, in.ScaleSetName) {
			if previous != nil {
				problem(w, http.StatusConflict, errors.New("duplicate organization scale-set configuration exists; resolve it before onboarding"))
				return
			}
			copy := candidate
			previous = &copy
			continue
		}
		if poolsHaveRoutingConflict(pool, candidate) {
			problem(w, http.StatusConflict, fmt.Errorf("scale-set label %q already routes an allowed repository through pool %q; choose a distinct label before migrating workflows", in.ScaleSetName, candidate.Name))
			return
		}
	}
	if previous != nil {
		before := *previous
		pool.ID = before.ID
		pool.MinIdle = before.MinIdle
		pool.ScaleSetDisabled = before.ScaleSetDisabled
		if in.Enabled != nil {
			pool.ScaleSetDisabled = !*in.Enabled
		}
		resetLearningForConfigChange(&before, &pool)
	} else if in.Enabled != nil && !*in.Enabled {
		pool.ScaleSetDisabled = true
	}
	if err := s.Store.UpsertPool(r.Context(), pool); err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	result := "enabled"
	detail := "organization pool saved; GitHub runner group and repository allowlist verified"
	if pool.ScaleSetDisabled {
		result, detail = "disabled", "organization scale-set listener disabled; existing workers may drain"
		_, _ = s.setScaleSetStatus(pool.ID, "disabled", detail)
	} else {
		_, _ = s.setScaleSetStatus(pool.ID, "pending", "waiting for organization scale-set listener")
	}
	_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "configure_organization_pool", ResourceType: "pool", ResourceID: pool.ID, Result: result, SafeDetail: in.Organization + "/" + in.ScaleSetName})
	writeJSON(w, http.StatusAccepted, pool)
}

func poolsHaveRoutingConflict(shared, existing core.RunnerPool) bool {
	sharesLabel := false
	for _, a := range shared.Labels {
		for _, b := range existing.Labels {
			if strings.EqualFold(a, b) {
				sharesLabel = true
				break
			}
		}
	}
	if !sharesLabel {
		return false
	}
	sharedRepos := make(map[string]bool, len(shared.AllowedRepositories))
	for _, repo := range shared.AllowedRepositories {
		sharedRepos[strings.ToLower(repo)] = true
	}
	if existing.Scope.Kind == core.ScopeRepository {
		return sharedRepos[strings.ToLower(existing.Scope.Owner+"/"+existing.Scope.Repository)]
	}
	if existing.Scope.Kind == core.ScopeOrganization && strings.EqualFold(shared.Scope.Owner, existing.Scope.Owner) {
		for _, repo := range existing.AllowedRepositories {
			if sharedRepos[strings.ToLower(repo)] {
				return true
			}
		}
	}
	return false
}

func (s *Service) disableOrganizationPool(w http.ResponseWriter, r *http.Request, organization, scaleSetName string) {
	pools, err := s.Store.ListPools(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	matches := make([]core.RunnerPool, 0, 1)
	for _, pool := range pools {
		if pool.Scope.Kind == core.ScopeOrganization && strings.EqualFold(pool.Scope.Owner, organization) && strings.EqualFold(pool.ScaleSetName, scaleSetName) {
			matches = append(matches, pool)
		}
	}
	if len(matches) == 0 {
		problem(w, http.StatusNotFound, errors.New("organization scale-set pool not found"))
		return
	}
	// A disable request targets the organization/scale-set identity. If stale
	// duplicate records exist, disable every matching listener so an arbitrary
	// first record cannot leave another duplicate active.
	for i := range matches {
		matches[i].ScaleSetDisabled = true
		if err := s.Store.UpsertPool(r.Context(), matches[i]); err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		detail := "organization scale-set listener disabled; existing workers may drain"
		_, _ = s.setScaleSetStatus(matches[i].ID, "disabled", detail)
		_ = s.Store.AppendAudit(r.Context(), core.AuditEvent{Actor: actor(r.Context()), Action: "configure_organization_pool", ResourceType: "pool", ResourceID: matches[i].ID, Result: "disabled", SafeDetail: organization + "/" + scaleSetName})
	}
	writeJSON(w, http.StatusAccepted, matches[0])
}

func (s *Service) validateOrganizationRepositories(r *http.Request, organization string, installationID int64, requested []string) ([]string, error) {
	canonical := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, raw := range requested {
		name := strings.TrimSpace(raw)
		if name == "" || seen[strings.ToLower(name)] {
			return nil, errors.New("repository allowlist contains an empty or duplicate repository")
		}
		seen[strings.ToLower(name)] = true
		repo, err := s.GitHubAuth.ValidateRepository(r, installationID, name)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(repo.Account, organization) || !strings.EqualFold(repo.AccountType, "Organization") {
			return nil, fmt.Errorf("repository %s is not owned by organization %s in the selected installation", name, organization)
		}
		if !repo.Private {
			return nil, fmt.Errorf("organization pool cannot include public repository %s", repo.FullName)
		}
		canonical = append(canonical, repo.FullName)
	}
	return canonical, nil
}

func trimStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	return out
}
