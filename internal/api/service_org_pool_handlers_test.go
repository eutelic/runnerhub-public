package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/store"
)

type orgPoolCatalog struct {
	group githubrunner.RunnerGroup
	repos []githubrunner.Repository
}

func (c orgPoolCatalog) ListRepositories(context.Context, core.GitHubScope) ([]githubrunner.Repository, error) {
	return append([]githubrunner.Repository(nil), c.repos...), nil
}
func (c orgPoolCatalog) GetRepository(context.Context, string, string) (githubrunner.Repository, error) {
	return githubrunner.Repository{}, nil
}
func (c orgPoolCatalog) ListRunnerGroups(context.Context, string) ([]githubrunner.RunnerGroup, error) {
	return []githubrunner.RunnerGroup{c.group}, nil
}
func (c orgPoolCatalog) GetRunnerGroup(_ context.Context, _ string, id int64) (githubrunner.RunnerGroup, error) {
	if id != c.group.ID {
		return githubrunner.RunnerGroup{}, store.ErrNotFound
	}
	return c.group, nil
}
func (c orgPoolCatalog) ListRunnerGroupRepositories(context.Context, string, int64) ([]githubrunner.Repository, error) {
	return append([]githubrunner.Repository(nil), c.repos...), nil
}

func TestOrganizationScaleSetConfigUsesOrgURLAndVerifiedGroup(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pool := core.RunnerPool{
		ID: "org-pool", Name: "org-acme-runnerhub",
		Scope:  core.GitHubScope{Kind: core.ScopeOrganization, Owner: "acme"},
		Labels: []string{"runnerhub"}, Isolation: core.IsolationContainerTrusted,
		Resources:      core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
		MaxConcurrency: 2, RunnerGroupID: 42, GitHubInstallationID: 7,
		ScaleSetName: "runnerhub", AllowedRepositories: []string{"acme/app"},
	}
	if err := db.UpsertPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	catalog := orgPoolCatalog{
		group: githubrunner.RunnerGroup{ID: 42, Name: "RunnerHub Shared", Visibility: "selected"},
		repos: []githubrunner.Repository{{ID: 5, FullName: "acme/app", Private: true}},
	}
	svc := &Service{Store: db, Catalog: catalog}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scalesets/configs", nil)
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var configs []scaleSetConfigResponse
	if err := json.NewDecoder(res.Body).Decode(&configs); err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].RegistrationURL != "https://github.com/acme" || configs[0].RunnerGroup != "RunnerHub Shared" {
		t.Fatalf("organization scale-set config = %+v", configs)
	}
}

func TestOrganizationScaleSetConfigFailsClosedForPublicRepository(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pool := core.RunnerPool{
		ID: "org-pool", Name: "org-acme-runnerhub",
		Scope:  core.GitHubScope{Kind: core.ScopeOrganization, Owner: "acme"},
		Labels: []string{"runnerhub"}, Isolation: core.IsolationContainerTrusted,
		Resources:      core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30},
		MaxConcurrency: 2, RunnerGroupID: 42, GitHubInstallationID: 7,
		ScaleSetName: "runnerhub", AllowedRepositories: []string{"acme/app"},
	}
	if err := db.UpsertPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	catalog := orgPoolCatalog{
		group: githubrunner.RunnerGroup{ID: 42, Name: "RunnerHub Shared", Visibility: "selected"},
		repos: []githubrunner.Repository{{ID: 5, FullName: "acme/app", Private: true}, {ID: 6, FullName: "acme/public", Private: false}},
	}
	svc := &Service{Store: db, Catalog: catalog}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scalesets/configs", nil)
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	var configs []scaleSetConfigResponse
	if err := json.NewDecoder(res.Body).Decode(&configs); err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 {
		t.Fatalf("unsafe organization scale set was published: %+v", configs)
	}
	svc.scaleSetStatusMu.RLock()
	got, ok := svc.scaleSetStatuses[pool.ID]
	svc.scaleSetStatusMu.RUnlock()
	if !ok || got.State != "error" {
		t.Fatalf("unsafe organization pool status = %+v, exists=%v", got, ok)
	}
}

func TestOrganizationPoolDetectsAmbiguousRepositoryLabels(t *testing.T) {
	shared := core.RunnerPool{
		Scope:  core.GitHubScope{Kind: core.ScopeOrganization, Owner: "acme"},
		Labels: []string{"runnerhub-org"}, AllowedRepositories: []string{"acme/app"},
	}
	dedicated := core.RunnerPool{
		Scope:  core.GitHubScope{Kind: core.ScopeRepository, Owner: "acme", Repository: "app"},
		Labels: []string{"runnerhub-org"},
	}
	if !poolsHaveRoutingConflict(shared, dedicated) {
		t.Fatal("shared and dedicated pools with an overlapping label were not detected")
	}
	dedicated.Labels = []string{"runnerhub-publisher"}
	if poolsHaveRoutingConflict(shared, dedicated) {
		t.Fatal("dedicated publisher label should not conflict with shared standard pool")
	}
}

func TestOrganizationPoolCanBeDisabledWithoutGitHubAvailability(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pool := core.RunnerPool{
		ID: "org-pool", Name: "org-acme-runnerhub-org",
		Scope:        core.GitHubScope{Kind: core.ScopeOrganization, Owner: "acme"},
		ScaleSetName: "runnerhub-org", RunnerGroupID: 42, MaxConcurrency: 2,
	}
	if err := db.UpsertPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: db}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/github/organization-pools", strings.NewReader(`{"organization":"acme","scaleSetName":"runnerhub-org","enabled":false}`))
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	updated, err := db.GetPool(ctx, pool.ID)
	if err != nil || !updated.ScaleSetDisabled {
		t.Fatalf("disabled pool=%+v err=%v", updated, err)
	}
}

func TestOrganizationPoolDisableTurnsOffEveryDuplicateRecord(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"org-pool-a", "org-pool-b"} {
		pool := core.RunnerPool{
			ID: id, Name: id,
			Scope:        core.GitHubScope{Kind: core.ScopeOrganization, Owner: "acme"},
			ScaleSetName: "runnerhub-org", MaxConcurrency: 2,
		}
		if err := db.UpsertPool(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{Store: db}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/github/organization-pools",
		strings.NewReader(`{"organization":"acme","scaleSetName":"runnerhub-org","enabled":false}`))
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, id := range []string{"org-pool-a", "org-pool-b"} {
		pool, err := db.GetPool(ctx, id)
		if err != nil || !pool.ScaleSetDisabled {
			t.Fatalf("pool %s disabled=%v err=%v", id, pool.ScaleSetDisabled, err)
		}
		svc.scaleSetStatusMu.RLock()
		status, ok := svc.scaleSetStatuses[id]
		svc.scaleSetStatusMu.RUnlock()
		if !ok || status.State != "disabled" {
			t.Fatalf("pool %s listener status=%+v exists=%v", id, status, ok)
		}
	}
}
