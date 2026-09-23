package dockergraph

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunnerHubUIOnlyInvalidatesControlImage(t *testing.T) {
	path := filepath.Join("..", "..", "..", "deploy", "docker", "Dockerfile")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, err := graph.AffectedTargets([]string{"web/app.js"}, "control", "agent", "deployer", "scaleset")
	if err != nil {
		t.Fatal(err)
	}
	if !got["control"] {
		t.Fatal("web assets must invalidate control image")
	}
	for _, target := range []string{"agent", "deployer", "scaleset"} {
		if got[target] {
			t.Fatalf("web-only change unexpectedly invalidates %s: %+v", target, got)
		}
	}
}

func TestSharedModuleMetadataInvalidatesRootGoImages(t *testing.T) {
	path := filepath.Join("..", "..", "..", "deploy", "docker", "Dockerfile")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, err := graph.AffectedTargets([]string{"go.mod"}, "control", "agent", "deployer")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"control", "agent", "deployer"} {
		if !got[target] {
			t.Fatalf("go.mod must invalidate %s: %+v", target, got)
		}
	}
}
