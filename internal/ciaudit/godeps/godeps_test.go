package godeps

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRunnerHubWebAssetsOnlyAffectControlGoTarget(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	graph, err := Load(context.Background(), root, map[string][]string{
		"control":  {"./cmd/runnerhubd"},
		"agent":    {"./cmd/runnerhub-agent"},
		"deployer": {"./cmd/runnerhub-deployer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	affected := graph.Affected([]string{"web/app.js"})
	if !affected["control"] {
		t.Fatal("embedded web/app.js must affect control")
	}
	if affected["agent"] || affected["deployer"] {
		t.Fatalf("UI-only input leaked into unrelated Go targets: %+v", affected)
	}
}

func TestGraphIncludesModuleAndTestInputs(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	graph, err := Load(context.Background(), root, map[string][]string{"agent": {"./cmd/runnerhub-agent"}})
	if err != nil {
		t.Fatal(err)
	}
	affected := graph.Affected([]string{"go.mod"})
	if !affected["agent"] {
		t.Fatal("go.mod must affect every root-module target")
	}
	if len(graph.Inputs("agent")) == 0 {
		t.Fatal("expected deterministic input evidence")
	}
}
