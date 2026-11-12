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
	var observed shellClassification
	for _, job := range workflow.Jobs {
		if job == nil || job.WorkflowCall != nil || len(job.Steps) == 0 {
			observed.unknown = true
			continue
		}
		for _, step := range job.Steps {
			if step == nil {
				continue
			}
			part, facts := classifyWorkflowExecution(step.Exec)
			observed.validation = observed.validation || part.validation
			observed.build = observed.build || part.build
			observed.deployment = observed.deployment || part.deployment
			observed.mutableSnapshot = observed.mutableSnapshot || part.mutableSnapshot
			observed.unknown = observed.unknown || part.unknown
			evidence = append(evidence, facts...)
		}
	}
	classification := WorkflowClassification{Evidence: uniqueSorted(evidence)}
	switch {
	case observed.deployment:
		classification.Purpose = PurposeDeployment
		classification.MutableSnapshotPublish = observed.mutableSnapshot
	case observed.unknown:
		classification.Purpose = PurposeUnknown
	case observed.validation:
		classification.Purpose = PurposeValidation
		classification.OrdinaryValidation = true
	case observed.build:
		classification.Purpose = PurposeBuild
	default:
		classification.Purpose = PurposeUnknown
	}
	return classification, nil
}

// classifyWorkflowExecution retains the fail-closed behavior for unrecognized
// step kinds while keeping shell and pinned-action semantics independent.
func classifyWorkflowExecution(execution any) (shellClassification, []string) {
	var facts []string
	var result shellClassification
	switch exec := execution.(type) {
	case *actionlint.ExecRun:
		if exec.Run == nil {
			result.unknown = true
			return result, nil
		}
		result = classifyShellScript(exec.Run.Value)
		if result.validation {
			facts = append(facts, "workflow contains a recognized test, lint, or static-analysis command")
		}
		if result.build {
			facts = append(facts, "workflow contains a recognized build command")
		}
		if result.deployment {
			facts = append(facts, "workflow contains a recognized publication or deployment command")
		}
		if result.mutableSnapshot {
			facts = append(facts, "publication targets a mutable latest or main tag")
		}
	case *actionlint.ExecAction:
		return classifyPinnedWorkflowAction(exec)
	default:
		result.unknown = true
	}
	return result, facts
}

// classifyPinnedWorkflowAction never certifies an unpinned/unknown action as
// ordinary validation. Mutating github-script and image pushes remain explicit.
func classifyPinnedWorkflowAction(exec *actionlint.ExecAction) (shellClassification, []string) {
	var result shellClassification
	var facts []string
	if exec == nil || exec.Uses == nil {
		result.unknown = true
		return result, nil
	}
	uses := strings.ToLower(strings.TrimSpace(exec.Uses.Value))
	at := strings.LastIndexByte(uses, '@')
	if at < 0 || !isFullGitSHA(uses[at+1:]) {
		result.unknown = true
		return result, nil
	}
	uses = uses[:at]
	switch {
	case uses == "actions/checkout" || strings.HasPrefix(uses, "actions/setup-") ||
		uses == "actions/cache" || uses == "actions/upload-artifact" ||
		uses == "actions/download-artifact" || uses == "docker/setup-buildx-action" ||
		uses == "docker/setup-qemu-action" || uses == "docker/setup-compose-action" ||
		uses == "docker/login-action" || uses == "docker/metadata-action":
		// Source/toolchain/cache/build preparation alone carries no purpose.
	case uses == "actions/github-script":
		lower := strings.ToLower(actionInput(exec, "script"))
		if containsCommand(lower, []string{
			".create(", ".update(", ".merge(", ".dispatch(", "createworkflowdispatch",
			"createworkflowrun", "delete", "updatebranch",
		}) {
			result.deployment = true
			facts = append(facts, "github-script contains a mutating API operation")
		} else {
			result.unknown = true
		}
	case uses == "docker/build-push-action":
		if strings.EqualFold(strings.TrimSpace(actionInput(exec, "push")), "true") {
			result.deployment = true
			facts = append(facts, "docker/build-push-action has push=true")
			tags := strings.ToLower(actionInput(exec, "tags"))
			if strings.Contains(tags, "latest") || strings.Contains(tags, "value=main") ||
				strings.Contains(tags, ":main") {
				result.mutableSnapshot = true
				facts = append(facts, "published image tags include a mutable latest or main tag")
			}
		} else {
			result.build = true
			facts = append(facts, "workflow builds an image without publishing it")
		}
	default:
		result.unknown = true
	}
	return result, facts
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
