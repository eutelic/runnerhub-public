package workflowparser

import (
	"reflect"
	"testing"
)

func TestFingerprintJobNormalizesFormattingAndIgnoresOtherJobs(t *testing.T) {
	first := []byte(`name: ci
on: [push, pull_request]
permissions:
  contents: read
jobs:
  core:
    runs-on: runnerhub
    steps:
      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803
      - run: go test ./...
  other:
    runs-on: runnerhub
    steps:
      - run: echo old
`)
	second := []byte(`# formatting and unrelated job change
name: ci
on:
  push:
  pull_request:
permissions: {contents: read}
jobs:
  other:
    runs-on: runnerhub
    steps:
      - run: echo changed
  core:
    runs-on: runnerhub
    steps:
      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803
      - run: "go test ./..."
`)
	a, err := FingerprintJob(first, "core")
	if err != nil {
		t.Fatal(err)
	}
	b, err := FingerprintJob(second, "core")
	if err != nil {
		t.Fatal(err)
	}
	if a.DefinitionHash != b.DefinitionHash {
		t.Fatalf("formatting/unrelated job changed definition hash: %s != %s", a.DefinitionHash, b.DefinitionHash)
	}

	changed := []byte(`name: ci
on: [push, pull_request]
permissions:
  contents: read
jobs:
  core:
    runs-on: runnerhub
    steps:
      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803
      - run: go test -race ./...
`)
	c, err := FingerprintJob(changed, "core")
	if err != nil {
		t.Fatal(err)
	}
	if a.DefinitionHash == c.DefinitionHash {
		t.Fatal("semantic job change did not change definition hash")
	}
}

func TestFingerprintJobExtractsExpressionContexts(t *testing.T) {
	source := []byte(`name: ci
on: [push]
jobs:
  build:
    runs-on: runnerhub
    strategy:
      matrix:
        goos: [darwin, windows]
    if: ${{ github.event_name == 'push' }}
    steps:
      - run: echo "${{ github.sha }} ${{ matrix.goos }} ${{ github['ref'] }}"
`)
	got, err := FingerprintJob(source, "build (darwin)")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"github.event_name", "github.ref", "github.sha", "matrix.goos"}
	if len(got.ContextReferences) != len(want) {
		t.Fatalf("refs=%v want=%v", got.ContextReferences, want)
	}
	for i := range want {
		if got.ContextReferences[i] != want[i] {
			t.Fatalf("refs=%v want=%v", got.ContextReferences, want)
		}
	}
}

func TestFingerprintJobFailsClosedForRuntimeEnvironmentAndMutableAction(t *testing.T) {
	source := []byte(`name: ci
on: [push]
jobs:
  build:
    runs-on: runnerhub
    steps:
      - uses: actions/checkout@v6
      - run: echo "$GITHUB_SHA $RUNNER_TEMP"
`)
	got, err := FingerprintJob(source, "build")
	if err != nil {
		t.Fatal(err)
	}
	if !got.UsesRuntimeEnvironment {
		t.Fatal("expected direct runner/GitHub environment use to be detected")
	}
	if !got.HasMutableAction {
		t.Fatal("expected mutable action reference to be detected")
	}
}

func TestFingerprintJobDetectsExternalFreshness(t *testing.T) {
	source := []byte(`name: ci
on: [push]
jobs:
  security:
    runs-on: runnerhub
    steps:
      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
`)
	got, err := FingerprintJob(source, "security")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExternalState {
		t.Fatal("expected vulnerability database dependency to use external freshness")
	}
}

func TestContextHashFailsClosedOnMissingReferencedContext(t *testing.T) {
	if _, err := ContextHash([]string{"github.sha"}, map[string]string{}, "core", "linux"); err == nil {
		t.Fatal("expected missing context value to fail")
	}
	first, err := ContextHash([]string{"github.sha"}, map[string]string{"github.sha": "abc"}, "core", "linux")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ContextHash([]string{"github.sha"}, map[string]string{"github.sha": "def"}, "core", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("context change did not change hash")
	}
}

func TestFingerprintJobRejectsMalformedWorkflow(t *testing.T) {
	if _, err := FingerprintJob([]byte("jobs: ["), "core"); err == nil {
		t.Fatal("expected malformed workflow to fail closed")
	}
}

func TestJobDependenciesUsesParsedNeedsGraph(t *testing.T) {
	source := []byte(`name: test
on: [push]
jobs:
  plan:
    runs-on: runnerhub
    steps:
      - run: echo plan
  test:
    needs: plan
    runs-on: runnerhub
    steps:
      - run: echo test
  publish:
    needs: [plan, test]
    runs-on: runnerhub
    steps:
      - run: echo publish
`)
	got, err := JobDependencies(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["plan"]) != 0 {
		t.Fatalf("plan needs=%v", got["plan"])
	}
	if want := []string{"plan"}; !reflect.DeepEqual(got["test"], want) {
		t.Fatalf("test needs=%v want=%v", got["test"], want)
	}
	if want := []string{"plan", "test"}; !reflect.DeepEqual(got["publish"], want) {
		t.Fatalf("publish needs=%v want=%v", got["publish"], want)
	}
}
