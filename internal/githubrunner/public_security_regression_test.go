package githubrunner

import (
	"context"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
)

// The public-repository feature is not enabled by choosing a nominally
// stronger isolation enum. Each supported mode must remain blocked until
// separate public/untrusted execution and assignment boundaries are proven.
func TestPublicRepositoryDeniedAcrossAllIsolationModes(t *testing.T) {
	modes := []core.IsolationMode{
		core.IsolationContainerTrusted,
		core.IsolationContainerDinD,
		core.IsolationHostPersistent,
		core.IsolationMicroVM,
		core.IsolationVMProvider,
	}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			p := core.RunnerPool{
				Scope: core.GitHubScope{
					Kind: core.ScopeRepository, Owner: "acme", Repository: "public",
				},
				Isolation: mode,
			}
			posture, err := InspectPoolSecurity(context.Background(), fakeCatalog{
				repo: Repository{FullName: "acme/public", Private: false},
			}, p)
			if err != nil {
				t.Fatal(err)
			}
			if posture.Safe || !strings.Contains(strings.Join(posture.Reasons, "; "), "public") {
				t.Fatalf("public repo unexpectedly accepted in %q: %+v", mode, posture)
			}
		})
	}
}

func TestOrganizationGroupRejectsPublicRepositoryEvenIfPublicFlagIsFalse(t *testing.T) {
	p := securePool()
	p.AllowedWorkflows = nil
	posture, err := InspectPoolSecurity(context.Background(), fakeCatalog{
		group: RunnerGroup{ID: 7, Visibility: "selected", AllowsPublicRepositories: false},
		repos: []Repository{
			{FullName: "acme/app", Private: true},
			{FullName: "acme/public", Private: false},
		},
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	if posture.Safe || !strings.Contains(strings.Join(posture.Reasons, "; "), "public repository") {
		t.Fatalf("selected group included a public repository: %+v", posture)
	}
}

func TestOrganizationGroupRejectsUnrecognizedVisibility(t *testing.T) {
	for _, visibility := range []string{"", "unknown", "private-only"} {
		t.Run("visibility="+visibility, func(t *testing.T) {
			p := securePool()
			p.AllowedRepositories = nil
			p.AllowedWorkflows = nil
			posture, err := InspectPoolSecurity(context.Background(), fakeCatalog{
				group: RunnerGroup{ID: 7, Visibility: visibility},
			}, p)
			if err != nil {
				t.Fatal(err)
			}
			if posture.Safe || !strings.Contains(strings.Join(posture.Reasons, "; "), "unknown visibility") {
				t.Fatalf("incomplete runner-group policy reported safe: %+v", posture)
			}
		})
	}
}

func TestOrganizationGroupRejectsMismatchedRunnerGroupID(t *testing.T) {
	p := securePool()
	p.AllowedRepositories = nil
	p.AllowedWorkflows = nil
	posture, err := InspectPoolSecurity(context.Background(), fakeCatalog{
		group: RunnerGroup{ID: 8, Visibility: "selected"},
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	if posture.Safe || !strings.Contains(strings.Join(posture.Reasons, "; "), "expected 7") {
		t.Fatalf("unexpected runner-group identity accepted: %+v", posture)
	}
}

func TestOrganizationGroupAcceptsKnownVisibilityWithoutPublicAccess(t *testing.T) {
	p := securePool()
	p.AllowedRepositories = nil
	p.AllowedWorkflows = nil
	posture, err := InspectPoolSecurity(context.Background(), fakeCatalog{
		group: RunnerGroup{ID: 7, Visibility: "all", AllowsPublicRepositories: false},
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	if !posture.Safe {
		t.Fatalf("valid all-private runner group rejected: %+v", posture)
	}
}

func TestInspectPoolSecurityRejectsUnknownScope(t *testing.T) {
	p := securePool()
	p.Scope.Kind = "unrecognized"
	_, err := InspectPoolSecurity(context.Background(), fakeCatalog{}, p)
	if err == nil {
		t.Fatal("invalid GitHub scope accepted")
	}
}
