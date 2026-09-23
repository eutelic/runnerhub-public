package workflowparser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainReuseWhitelistIsEventIndependent(t *testing.T) {
	workflowPath := filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml")
	raw, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{
		"core",
		"Minimum declared Go toolchains",
		"scaleset",
		"agent-crossbuild (darwin)",
		"agent-crossbuild (windows)",
	} {
		fp, err := FingerprintJob(raw, job)
		if err != nil {
			t.Fatalf("%s: %v", job, err)
		}
		if fp.UsesRuntimeEnvironment || fp.HasMutableAction || fp.HasOpaqueAction {
			t.Fatalf("%s is no longer safe for tree-identical PR-to-main reuse: %+v", job, fp)
		}
		for _, ref := range fp.ContextReferences {
			lower := strings.ToLower(strings.TrimSpace(ref))
			if strings.HasPrefix(lower, "github.") {
				t.Fatalf("%s now depends on event/ref/SHA context %q; remove it from the reuse whitelist", job, ref)
			}
		}
	}
}
