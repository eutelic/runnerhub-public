package workflowparser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/rhysd/actionlint"
)

const ParserVersion = "actionlint-v1.7.12-semantic-v2"

type JobFingerprint struct {
	JobID                  string   `json:"jobId"`
	DisplayName            string   `json:"displayName"`
	DefinitionHash         string   `json:"definitionHash"`
	ContextReferences      []string `json:"contextReferences,omitempty"`
	UsesRuntimeEnvironment bool     `json:"usesRuntimeEnvironment,omitempty"`
	HasMutableAction       bool     `json:"hasMutableAction,omitempty"`
	HasOpaqueAction        bool     `json:"hasOpaqueAction,omitempty"`
	ExternalState          bool     `json:"externalState,omitempty"`
}

// FingerprintJob parses a GitHub Actions workflow using actionlint's syntax
// tree, selects one expanded GitHub job by its display name, and returns a
// normalized semantic definition hash plus the expression contexts it reads.
//
// Source positions and YAML quoting style are deliberately excluded from the
// definition hash. Unknown/malformed workflows fail closed.
func FingerprintJob(source []byte, githubJobName string) (JobFingerprint, error) {
	workflow, parseErrors := actionlint.Parse(source)
	if len(parseErrors) > 0 || workflow == nil {
		if len(parseErrors) > 0 {
			return JobFingerprint{}, fmt.Errorf("parse workflow: %s", parseErrors[0])
		}
		return JobFingerprint{}, errors.New("parse workflow: no syntax tree")
	}
	job, err := findJob(workflow, githubJobName)
	if err != nil {
		return JobFingerprint{}, err
	}

	material := map[string]any{
		"parserVersion": ParserVersion,
		"permissions":   normalize(reflect.ValueOf(workflow.Permissions)),
		"env":           normalize(reflect.ValueOf(workflow.Env)),
		"defaults":      normalize(reflect.ValueOf(workflow.Defaults)),
		"job":           normalize(reflect.ValueOf(job)),
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return JobFingerprint{}, fmt.Errorf("marshal normalized workflow job: %w", err)
	}

	stringsInScope := collectStrings(reflect.ValueOf(materialForReferences(workflow, job)))
	references := map[string]struct{}{}
	usesRuntimeEnv := false
	externalState := false
	for _, value := range stringsInScope {
		refs, err := expressionReferences(value)
		if err != nil {
			return JobFingerprint{}, err
		}
		for _, ref := range refs {
			references[ref] = struct{}{}
		}
		upper := strings.ToUpper(value)
		if strings.Contains(upper, "GITHUB_") || strings.Contains(upper, "RUNNER_") {
			usesRuntimeEnv = true
		}
		lower := strings.ToLower(value)
		for _, marker := range []string{"govulncheck", "npm audit", "pnpm audit", "yarn audit", "pip-audit", "osv-scanner", "trivy ", "grype "} {
			if strings.Contains(lower, marker) {
				externalState = true
				break
			}
		}
	}

	refs := make([]string, 0, len(references))
	for ref := range references {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	mutableAction, opaqueAction, actionExternalState := actionPolicy(job)
	externalState = externalState || actionExternalState

	return JobFingerprint{
		JobID:                  job.ID.Value,
		DisplayName:            baseDisplayName(job),
		DefinitionHash:         hashParts(ParserVersion, string(encoded)),
		ContextReferences:      refs,
		UsesRuntimeEnvironment: usesRuntimeEnv,
		HasMutableAction:       mutableAction,
		HasOpaqueAction:        opaqueAction,
		ExternalState:          externalState,
	}, nil
}

func materialForReferences(workflow *actionlint.Workflow, job *actionlint.Job) map[string]any {
	return map[string]any{
		"env":      workflow.Env,
		"defaults": workflow.Defaults,
		"job":      job,
	}
}

func findJob(workflow *actionlint.Workflow, githubJobName string) (*actionlint.Job, error) {
	name := strings.TrimSpace(githubJobName)
	if name == "" {
		return nil, errors.New("GitHub job name is required")
	}
	var matches []*actionlint.Job
	for _, job := range workflow.Jobs {
		if job == nil || job.ID == nil || strings.TrimSpace(job.ID.Value) == "" {
			continue
		}
		base := baseDisplayName(job)
		if name == base || strings.HasPrefix(name, base+" (") {
			matches = append(matches, job)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("workflow job %q was not found in parsed workflow", name)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("workflow job %q is ambiguous in parsed workflow", name)
	}
	return matches[0], nil
}

func baseDisplayName(job *actionlint.Job) string {
	if job.Name != nil && strings.TrimSpace(job.Name.Value) != "" {
		return strings.TrimSpace(job.Name.Value)
	}
	if job.ID == nil {
		return ""
	}
	return strings.TrimSpace(job.ID.Value)
}

func checkoutUsesExternalState(action *actionlint.ExecAction) bool {
	if action == nil {
		return true
	}
	for _, name := range []string{"repository", "ref", "submodules", "lfs"} {
		input := action.Inputs[name]
		if input == nil || input.Value == nil {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(input.Value.Value))
		if value == "" {
			continue
		}
		if name == "submodules" || name == "lfs" {
			if value == "false" || value == "no" {
				continue
			}
			return true
		}
		// Explicit repository/ref values are not represented by the current
		// repository's HeadTreeSHA, even when they happen to resolve to it.
		return true
	}
	return false
}

func actionPolicy(job *actionlint.Job) (mutable, opaque, externalState bool) {
	for _, step := range job.Steps {
		if step == nil || step.Exec == nil {
			continue
		}
		action, ok := step.Exec.(*actionlint.ExecAction)
		if !ok || action.Uses == nil {
			continue
		}
		uses := strings.TrimSpace(action.Uses.Value)
		if uses == "" {
			continue
		}
		if strings.HasPrefix(uses, "./") || strings.HasPrefix(uses, "docker://") {
			opaque = true
			continue
		}
		at := strings.LastIndexByte(uses, '@')
		if at < 0 || !isFullGitSHA(uses[at+1:]) {
			mutable = true
			continue
		}
		repository := strings.ToLower(uses[:at])
		switch repository {
		case "actions/checkout":
			// The checked-out source tree is covered by input_hash only when it
			// comes from this run's repository tree. Submodules, LFS objects,
			// alternate repositories, and alternate refs add external inputs.
			if checkoutUsesExternalState(action) {
				externalState = true
			}
		case "actions/setup-go", "actions/setup-node", "actions/setup-python", "actions/setup-java", "actions/setup-dotnet":
			// These pinned setup actions have a bounded, explicit interface, but
			// resolve toolchain binaries outside the repository tree. Preserve the
			// job definition in the fingerprint and require external freshness.
			externalState = true
		case "actions/cache", "actions/cache/restore", "actions/cache/save":
			// Cache contents are external inputs even when the action and key
			// are pinned in the workflow. Keep the action definition in the hash
			// and require freshness evidence before reusing a validation result.
			externalState = true
		default:
			opaque = true
		}
	}
	if job.WorkflowCall != nil && job.WorkflowCall.Uses != nil {
		uses := strings.TrimSpace(job.WorkflowCall.Uses.Value)
		if strings.HasPrefix(uses, "./") {
			opaque = true
			return
		}
		at := strings.LastIndexByte(uses, '@')
		if at < 0 || !isFullGitSHA(uses[at+1:]) {
			mutable = true
		} else {
			opaque = true
		}
	}
	return
}

func isFullGitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func expressionReferences(value string) ([]string, error) {
	refs := map[string]struct{}{}
	rest := value
	for {
		start := strings.Index(rest, "${{")
		if start < 0 {
			break
		}
		lexer := actionlint.NewExprLexer(rest[start+3:])
		node, err := actionlint.NewExprParser().Parse(lexer)
		if err != nil {
			return nil, fmt.Errorf("parse workflow expression: %w", err)
		}
		actionlint.VisitExprNode(node, func(node, parent actionlint.ExprNode, entering bool) {
			if !entering {
				return
			}
			path, ok := staticPath(node)
			if !ok {
				return
			}
			if parentPath, parentOK := staticPath(parent); parentOK && strings.HasPrefix(parentPath, path+".") {
				return
			}
			refs[path] = struct{}{}
		})
		consumed := lexer.Offset()
		if consumed <= 0 || start+3+consumed > len(rest) {
			return nil, errors.New("workflow expression parser made no progress")
		}
		rest = rest[start+3+consumed:]
	}
	out := make([]string, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out, nil
}

func staticPath(node actionlint.ExprNode) (string, bool) {
	if node == nil {
		return "", false
	}
	switch n := node.(type) {
	case *actionlint.VariableNode:
		return n.Name, n.Name != ""
	case *actionlint.ObjectDerefNode:
		receiver, ok := staticPath(n.Receiver)
		if !ok || n.Property == "" {
			return "", false
		}
		return receiver + "." + n.Property, true
	case *actionlint.IndexAccessNode:
		receiver, ok := staticPath(n.Operand)
		if !ok {
			return "", false
		}
		index, ok := n.Index.(*actionlint.StringNode)
		if !ok || index.Value == "" {
			return "", false
		}
		return receiver + "." + index.Value, true
	default:
		return "", false
	}
}

func collectStrings(value reflect.Value) []string {
	var out []string
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		if !v.IsValid() {
			return
		}
		if v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return
			}
			walk(v.Elem())
			return
		}
		if v.Type() == reflect.TypeOf(actionlint.String{}) {
			out = append(out, v.FieldByName("Value").String())
			return
		}
		switch v.Kind() {
		case reflect.Struct:
			t := v.Type()
			for i := 0; i < v.NumField(); i++ {
				if t.Field(i).PkgPath != "" {
					continue
				}
				walk(v.Field(i))
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				walk(iter.Value())
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		case reflect.String:
			out = append(out, v.String())
		}
	}
	walk(value)
	return out
}

func normalize(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return normalize(v.Elem())
	}
	if v.Type() == reflect.TypeOf(actionlint.Pos{}) {
		return nil
	}
	if v.Type() == reflect.TypeOf(actionlint.String{}) {
		return map[string]any{"value": v.FieldByName("Value").String()}
	}
	switch v.Kind() {
	case reflect.Struct:
		out := map[string]any{}
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" || field.Name == "Pos" || field.Name == "RunPos" || field.Name == "Quoted" {
				continue
			}
			out[field.Name] = normalize(v.Field(i))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		out := map[string]any{}
		iter := v.MapRange()
		for iter.Next() {
			key := iter.Key()
			if key.Kind() != reflect.String {
				continue
			}
			out[key.String()] = normalize(iter.Value())
		}
		return out
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = normalize(v.Index(i))
		}
		return out
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	default:
		return nil
	}
}

// ContextHash hashes only context values referenced by the parsed job. Missing
// values fail closed. Stable parts are always included and are intended for
// execution properties such as expanded job name and runner OS.
func ContextHash(references []string, values map[string]string, stableParts ...string) (string, error) {
	parts := []string{ParserVersion}
	refs := append([]string(nil), references...)
	sort.Strings(refs)
	for _, ref := range refs {
		value, ok := values[ref]
		if !ok {
			return "", fmt.Errorf("context %q is not available for deterministic identity", ref)
		}
		parts = append(parts, ref, value)
	}
	parts = append(parts, stableParts...)
	return hashParts(parts...), nil
}

func hashParts(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		raw := []byte(part)
		var size [8]byte
		n := uint64(len(raw))
		for i := 7; i >= 0; i-- {
			size[i] = byte(n)
			n >>= 8
		}
		_, _ = h.Write(size[:])
		_, _ = h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// JobDependencies returns the normalized GitHub Actions needs DAG from the same
// pinned actionlint syntax tree used for semantic fingerprints.
func JobDependencies(source []byte) (map[string][]string, error) {
	workflow, parseErrors := actionlint.Parse(source)
	if len(parseErrors) > 0 || workflow == nil {
		if len(parseErrors) > 0 {
			return nil, fmt.Errorf("parse workflow: %s", parseErrors[0])
		}
		return nil, errors.New("parse workflow: no syntax tree")
	}
	out := make(map[string][]string, len(workflow.Jobs))
	for _, job := range workflow.Jobs {
		if job == nil || job.ID == nil || strings.TrimSpace(job.ID.Value) == "" {
			continue
		}
		id := strings.TrimSpace(job.ID.Value)
		seen := map[string]struct{}{}
		var deps []string
		for _, raw := range collectStrings(reflect.ValueOf(job.Needs)) {
			dep := strings.TrimSpace(raw)
			if dep == "" || dep == id {
				continue
			}
			if _, ok := seen[dep]; ok {
				continue
			}
			seen[dep] = struct{}{}
			deps = append(deps, dep)
		}
		sort.Strings(deps)
		out[id] = deps
	}
	return out, nil
}
