package ciaudit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/ciaudit/dockergraph"
)

func repoFile(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", ".."}, parts...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRunnerHubConcreteCIWasteRegressions(t *testing.T) {
	publish := repoFile(t, ".github", "workflows", "publish-images.yml")
	if strings.Contains(publish, "validate-release-local.sh") {
		t.Fatal("publish-images must not rerun ordinary/local source validation")
	}
	if !strings.Contains(publish, "Verify staged published artifacts") || !strings.Contains(publish, "linux/amd64,linux/arm64") {
		t.Fatal("publication must verify staged artifacts and preserve multi-architecture builds")
	}
	if !strings.Contains(publish, "git ls-remote origin refs/heads/main") || !strings.Contains(publish, "refusing to move latest") {
		t.Fatal("mutable latest promotion must fail safe when main advanced")
	}
	if !strings.Contains(publish, "cancel-in-progress:") || !strings.Contains(publish, "github.ref_type") {
		t.Fatal("branch snapshots must be supersedable while immutable tag publication remains protected")
	}

	if _, err := os.Stat(filepath.Join("..", "..", ".github", "workflows", "secret-init-ci.yml")); !os.IsNotExist(err) {
		t.Fatal("duplicate standalone secret-init workflow must remain consolidated")
	}
	if _, err := os.Stat(filepath.Join("..", "..", ".github", "workflows", "truenas-compose-prep-ci.yml")); !os.IsNotExist(err) {
		t.Fatal("duplicate standalone TrueNAS-tools workflow must remain consolidated")
	}

	dind := repoFile(t, ".github", "workflows", "dind-runtime-ci.yml")
	if strings.Contains(dind, "go test ./...") {
		t.Fatal("DinD integration lane must not rerun generic unit coverage")
	}

	dockerfile := repoFile(t, "deploy", "docker", "Dockerfile")
	graph, err := dockergraph.Parse(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	affected, err := graph.AffectedTargets([]string{"web/app.js"}, "control", "agent", "deployer", "scaleset")
	if err != nil {
		t.Fatal(err)
	}
	if !affected["control"] || affected["agent"] || affected["deployer"] || affected["scaleset"] {
		t.Fatalf("UI-only Docker rebuild scope regressed: %+v", affected)
	}

	planner := repoFile(t, "scripts", "ci-changes.sh")
	if !strings.Contains(planner, "web/*|cmd/runnerhubd/*|migrations/*") {
		t.Fatal("CI planner must retain explicit control/UI-only classification")
	}
}
