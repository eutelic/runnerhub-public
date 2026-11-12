package workflowparser

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/rhysd/actionlint"
)

type WorkflowPurpose string

const (
	PurposeUnknown    WorkflowPurpose = "unknown"
	PurposeValidation WorkflowPurpose = "validation"
	PurposeBuild      WorkflowPurpose = "build"
	PurposeDeployment WorkflowPurpose = "deployment"
)

type WorkflowClassification struct {
	Purpose                WorkflowPurpose
	OrdinaryValidation     bool
	MutableSnapshotPublish bool
	Evidence               []string
}

// ClassifyWorkflow derives a conservative purpose from workflow job steps.
// Workflow and job names are deliberately ignored. Unknown actions or
// unrecognized workflows fail closed so the result cannot certify side effects.
func ClassifyWorkflow(source []byte) (WorkflowClassification, error) {
	workflow, parseErrors := actionlint.Parse(source)
	if len(parseErrors) > 0 || workflow == nil {
		if len(parseErrors) > 0 {
			return WorkflowClassification{}, fmt.Errorf("parse workflow for purpose: %s", parseErrors[0])
		}
		return WorkflowClassification{}, errors.New("parse workflow for purpose: no syntax tree")
	}
	if len(workflow.Jobs) == 0 {
		return WorkflowClassification{}, errors.New("classify workflow purpose: no jobs")
	}

	var evidence []string
	hasValidation, hasBuild, hasDeployment, mutableSnapshot, hasUnknown := false, false, false, false, false
	for _, job := range workflow.Jobs {
		if job == nil || job.WorkflowCall != nil || len(job.Steps) == 0 {
			hasUnknown = true
			continue
		}
		for _, step := range job.Steps {
			if step == nil {
				continue
			}
			switch exec := step.Exec.(type) {
			case *actionlint.ExecRun:
				if exec.Run == nil {
					hasUnknown = true
					continue
				}
				classification := classifyShellScript(exec.Run.Value)
				hasValidation = hasValidation || classification.validation
				hasBuild = hasBuild || classification.build
				hasDeployment = hasDeployment || classification.deployment
				hasUnknown = hasUnknown || classification.unknown
				mutableSnapshot = mutableSnapshot || classification.mutableSnapshot
				if classification.validation {
					evidence = append(evidence, "workflow contains a recognized test, lint, or static-analysis command")
				}
				if classification.build {
					evidence = append(evidence, "workflow contains a recognized build command")
				}
				if classification.deployment {
					evidence = append(evidence, "workflow contains a recognized publication or deployment command")
				}
				if classification.mutableSnapshot {
					evidence = append(evidence, "publication targets a mutable latest or main tag")
				}
			case *actionlint.ExecAction:
				if exec.Uses == nil {
					hasUnknown = true
					continue
				}
				uses := strings.ToLower(strings.TrimSpace(exec.Uses.Value))
				at := strings.LastIndexByte(uses, '@')
				if at < 0 || !isFullGitSHA(uses[at+1:]) {
					hasUnknown = true
					continue
				}
				uses = uses[:at]
				switch {
				case uses == "actions/checkout" || strings.HasPrefix(uses, "actions/setup-") ||
					uses == "actions/cache" || uses == "actions/upload-artifact" ||
					uses == "actions/download-artifact" || uses == "docker/setup-buildx-action" ||
					uses == "docker/setup-qemu-action" || uses == "docker/setup-compose-action" ||
					uses == "docker/login-action" ||
					uses == "docker/metadata-action":
					// These actions prepare source, a toolchain, a cache, or build metadata.
				case uses == "actions/github-script":
					script := actionInput(exec, "script")
					lower := strings.ToLower(script)
					if containsCommand(lower, []string{
						".create(", ".update(", ".merge(", ".dispatch(", "createworkflowdispatch",
						"createworkflowrun", "delete", "updatebranch",
					}) {
						hasDeployment = true
						evidence = append(evidence, "github-script contains a mutating API operation")
					} else {
						// Arbitrary JavaScript can perform side effects that this
						// classifier cannot safely infer from source substrings.
						hasUnknown = true
					}
				case uses == "docker/build-push-action":
					push := strings.EqualFold(strings.TrimSpace(actionInput(exec, "push")), "true")
					if push {
						hasDeployment = true
						evidence = append(evidence, "docker/build-push-action has push=true")
						tags := strings.ToLower(actionInput(exec, "tags"))
						if strings.Contains(tags, "latest") || strings.Contains(tags, "value=main") ||
							strings.Contains(tags, ":main") {
							mutableSnapshot = true
							evidence = append(evidence, "published image tags include a mutable latest or main tag")
						}
					} else {
						hasBuild = true
						evidence = append(evidence, "workflow builds an image without publishing it")
					}
				default:
					hasUnknown = true
				}
			default:
				// Unknown step kinds may have side effects that cannot be classified.
				hasUnknown = true
			}
		}
	}
	classification := WorkflowClassification{Evidence: uniqueSorted(evidence)}
	switch {
	case hasDeployment:
		classification.Purpose = PurposeDeployment
		classification.MutableSnapshotPublish = mutableSnapshot
	case hasUnknown:
		classification.Purpose = PurposeUnknown
	case hasValidation:
		classification.Purpose = PurposeValidation
		classification.OrdinaryValidation = true
	case hasBuild:
		classification.Purpose = PurposeBuild
	default:
		classification.Purpose = PurposeUnknown
	}
	return classification, nil
}

type shellClassification struct {
	validation      bool
	build           bool
	deployment      bool
	mutableSnapshot bool
	unknown         bool
}

// classifyShellScript only trusts simple, single shell commands from a small
// allowlist. Compound commands and shell syntax can hide side effects, so they
// fail closed instead of letting a test command certify the whole workflow.
func classifyShellScript(script string) shellClassification {
	var result shellClassification
	seenCommand := false
	validationCommands := []string{
		"go test", "go vet", "node --test", "npm test", "pnpm test", "yarn test",
		"pytest", "cargo test", "gofmt -d", "actionlint", "staticcheck",
		"gosec", "govulncheck", "sh -n", "bash -n", "shellcheck",
	}
	buildCommands := []string{
		"go build", "npm run build", "pnpm build", "docker build", "cargo build",
		"dotnet build", "make build",
	}
	deploymentCommands := []string{
		"docker push", "crane tag", "gh release create", "gh release upload",
		"terraform apply", "kubectl apply", "/api/v1/deployments/",
	}
	for _, rawLine := range strings.Split(script, "\n") {
		line := strings.TrimSpace(strings.ToLower(rawLine))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		seenCommand = true
		if strings.ContainsAny(line, ";|&$()<>\x60\\") {
			result.unknown = true
			continue
		}
		if matchesSimpleCommand(line, validationCommands) {
			result.validation = true
			continue
		}
		if matchesSimpleCommand(line, buildCommands) {
			result.build = true
			continue
		}
		if matchesSimpleCommand(line, deploymentCommands) {
			result.deployment = true
			if strings.HasPrefix(line, "crane tag ") &&
				(strings.Contains(line, "latest") || strings.Contains(line, ":main")) {
				result.mutableSnapshot = true
			}
			continue
		}
		if strings.HasPrefix(line, "curl ") && strings.Contains(line, " -x post ") {
			result.deployment = true
			continue
		}
		result.unknown = true
	}
	if !seenCommand {
		result.unknown = true
	}
	return result
}

func matchesSimpleCommand(line string, patterns []string) bool {
	for _, pattern := range patterns {
		if line == pattern || strings.HasPrefix(line, pattern+" ") {
			return true
		}
	}
	return false
}

func actionInput(action *actionlint.ExecAction, name string) string {
	if action == nil {
		return ""
	}
	input := action.Inputs[strings.ToLower(name)]
	if input == nil || input.Value == nil {
		return ""
	}
	return input.Value.Value
}

func containsCommand(source string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(source, pattern) {
			return true
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
