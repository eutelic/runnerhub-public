package ciaudit

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ArtifactBuildEvidence captures the semantic Buildx/OCI identity needed to
// distinguish a real duplicate build from two superficially similar commands.
type ArtifactBuildEvidence struct {
	BuildKey         string    `json:"buildKey"`
	DefinitionHash   string    `json:"definitionHash"`
	InputHash        string    `json:"inputHash"`
	ContextHash      string    `json:"contextHash"`
	Target           string    `json:"target,omitempty"`
	Platforms        []string  `json:"platforms,omitempty"`
	BuildArgs        []string  `json:"buildArgs,omitempty"`
	ArtifactDigest   string    `json:"artifactDigest,omitempty"`
	ProvenanceDigest string    `json:"provenanceDigest,omitempty"`
	CompletedAt      time.Time `json:"completedAt"`
}

func (b ArtifactBuildEvidence) Key() (string, error) {
	if strings.TrimSpace(b.DefinitionHash) == "" || strings.TrimSpace(b.InputHash) == "" || strings.TrimSpace(b.ContextHash) == "" {
		return "", fmt.Errorf("build identity requires definition, input, and context hashes")
	}
	platforms := append([]string(nil), b.Platforms...)
	args := append([]string(nil), b.BuildArgs...)
	sort.Strings(platforms)
	sort.Strings(args)
	return HashParts(
		"artifact-build-v1",
		strings.TrimSpace(b.DefinitionHash),
		strings.TrimSpace(b.InputHash),
		strings.TrimSpace(b.ContextHash),
		strings.TrimSpace(b.Target),
		strings.Join(platforms, "\x00"),
		strings.Join(args, "\x00"),
	), nil
}

// DuplicateArtifactBuildFinding returns a provable duplicate when identical
// semantic build identity and artifact digest already existed before execution.
// Without a digest it remains a separate high-confidence potential saving.
func DuplicateArtifactBuildFinding(job JobExecution, current, prior ArtifactBuildEvidence) (Finding, bool) {
	if !validJob(job) || prior.CompletedAt.IsZero() || !prior.CompletedAt.Before(job.StartedAt) {
		return Finding{}, false
	}
	currentKey, err := current.Key()
	if err != nil {
		return Finding{}, false
	}
	priorKey, err := prior.Key()
	if err != nil || currentKey != priorKey {
		return Finding{}, false
	}
	class := EvidenceHighConfidence
	detector := "duplicate-artifact-build"
	evidence := []string{
		fmt.Sprintf("build identity %s already completed at %s", currentKey, prior.CompletedAt.UTC().Format(time.RFC3339Nano)),
	}
	if digest := strings.TrimSpace(current.ArtifactDigest); digest != "" && digest == strings.TrimSpace(prior.ArtifactDigest) {
		class = EvidenceInputEquivalent
		evidence = append(evidence, fmt.Sprintf("published artifact digest matched %s", digest))
	} else {
		evidence = append(evidence, "artifact digest was unavailable or differed; classify as potential savings only")
	}
	return Finding{
		JobID: job.ID, DetectorID: detector, Class: class, AnalysisVersion: AnalyzerVersion,
		Interval: Interval{Start: job.StartedAt, End: job.CompletedAt}, Evidence: evidence,
	}, true
}

// DependencyUnaffectedFinding records a fully avoidable execution only when an
// external dependency adapter has proved that no changed path reaches the job's
// tested/built target.
func DependencyUnaffectedFinding(job JobExecution, target string, changed []string, proof string) (Finding, bool) {
	if !validJob(job) || strings.TrimSpace(target) == "" || strings.TrimSpace(proof) == "" {
		return Finding{}, false
	}
	evidence := []string{
		fmt.Sprintf("target %q was proven unaffected by the change set", target),
		strings.TrimSpace(proof),
	}
	if len(changed) > 0 {
		copyChanged := append([]string(nil), changed...)
		sort.Strings(copyChanged)
		evidence = append(evidence, "changed paths: "+strings.Join(copyChanged, ", "))
	}
	return Finding{
		JobID: job.ID, DetectorID: "dependency-unaffected", Class: EvidenceDependencyProven,
		AnalysisVersion: AnalyzerVersion, Interval: Interval{Start: job.StartedAt, End: job.CompletedAt},
		Evidence: evidence,
	}, true
}
