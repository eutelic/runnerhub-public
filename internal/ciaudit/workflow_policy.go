package ciaudit

import "strings"

// OrdinaryValidationWorkflow reports whether RunnerHub has an explicit,
// repository-owned policy that a workflow is ordinary validation with no
// independent publish/deploy value. Keep this allowlist aligned with
// cleanup-merged-pr-runs.yml; unknown workflows fail closed.
func OrdinaryValidationWorkflow(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ci",
		"deployment-ci",
		"dind-runtime-ci",
		"secret-init-ci",
		"truenas-deploy-tools-ci",
		"truenas-compose-prep-ci",
		"hardening-validation":
		return true
	default:
		return false
	}
}

// MutableSnapshotPublicationWorkflow reports whether a workflow publishes a
// replaceable branch snapshot. This is deliberately separate from ordinary
// validation because publication is side-effecting and must never be canceled
// by the generic validation policy.
func MutableSnapshotPublicationWorkflow(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "publish-images")
}
