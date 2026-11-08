package core

import "testing"

func TestValidateOrganizationAllowlistUsesPinnedWorkflowRefs(t *testing.T) {
	p := RunnerPool{Name: "secure", Scope: GitHubScope{Kind: ScopeOrganization, Owner: "acme"}, Labels: []string{"secure"}, Isolation: IsolationContainerTrusted, MaxConcurrency: 1, RunnerGroupID: 2, AllowedRepositories: []string{"acme/app"}, AllowedWorkflows: []string{"acme/app/.github/workflows/build.yml@main"}}
	if err := ValidateRunnerPool(p); err != nil {
		t.Fatal(err)
	}
	p.AllowedWorkflows = []string{"acme/app/.github/workflows/build.yml"}
	if ValidateRunnerPool(p) == nil {
		t.Fatal("unpinned workflow ref accepted")
	}
	p.AllowedWorkflows = []string{"other/app/.github/workflows/build.yml@main"}
	if ValidateRunnerPool(p) == nil {
		t.Fatal("cross-organization workflow accepted")
	}
}
