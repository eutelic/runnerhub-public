package ciaudit

import (
	"testing"
	"time"
)

func TestDuplicateArtifactBuildUsesDigestForProvableClass(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	job := JobExecution{ID: "build-2", StartedAt: base.Add(10 * time.Minute), CompletedAt: base.Add(15 * time.Minute)}
	prior := ArtifactBuildEvidence{
		DefinitionHash: "def", InputHash: "input", ContextHash: "ctx", Target: "control",
		Platforms: []string{"linux/arm64", "linux/amd64"}, BuildArgs: []string{"VERSION=abc"},
		ArtifactDigest: "sha256:deadbeef", CompletedAt: base,
	}
	current := prior
	current.Platforms = []string{"linux/amd64", "linux/arm64"}
	current.CompletedAt = job.CompletedAt
	got, ok := DuplicateArtifactBuildFinding(job, current, prior)
	if !ok || got.Class != EvidenceInputEquivalent {
		t.Fatalf("finding=%+v ok=%v", got, ok)
	}
}

func TestDuplicateArtifactBuildWithoutDigestIsPotentialOnly(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	job := JobExecution{ID: "build", StartedAt: base.Add(time.Minute), CompletedAt: base.Add(2 * time.Minute)}
	prior := ArtifactBuildEvidence{DefinitionHash: "d", InputHash: "i", ContextHash: "c", CompletedAt: base}
	current := prior
	current.CompletedAt = job.CompletedAt
	got, ok := DuplicateArtifactBuildFinding(job, current, prior)
	if !ok || got.Class != EvidenceHighConfidence {
		t.Fatalf("finding=%+v ok=%v", got, ok)
	}
}

func TestDependencyUnaffectedFindingIsProvable(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	job := JobExecution{ID: "agent", StartedAt: base, CompletedAt: base.Add(time.Minute)}
	got, ok := DependencyUnaffectedFinding(job, "runnerhub-agent", []string{"web/app.js"}, "Go embed/package graph reaches runnerhubd only")
	if !ok || got.Class != EvidenceDependencyProven {
		t.Fatalf("finding=%+v ok=%v", got, ok)
	}
}
