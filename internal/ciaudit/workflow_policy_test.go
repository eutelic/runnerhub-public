package ciaudit

import "testing"

func TestOrdinaryValidationWorkflowFailsClosed(t *testing.T) {
	for _, name := range []string{
		"ci",
		"deployment-ci",
		"dind-runtime-ci",
		"secret-init-ci",
		"truenas-deploy-tools-ci",
		"truenas-compose-prep-ci",
		"hardening-validation",
	} {
		if !OrdinaryValidationWorkflow(name) {
			t.Fatalf("expected %q to be ordinary validation", name)
		}
	}
	for _, name := range []string{"publish-images", "deploy-production", "release", "", "unknown"} {
		if OrdinaryValidationWorkflow(name) {
			t.Fatalf("workflow %q must fail closed", name)
		}
	}
}
