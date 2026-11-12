package workflowparser

import "testing"

func TestClassifyWorkflowPurposeUsesStepEvidenceNotNames(t *testing.T) {
	validation, err := ClassifyWorkflow([]byte(`name: publish-images
on: [push]
jobs:
  checks:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@0123456789012345678901234567890123456789
      - uses: actions/setup-go@0123456789012345678901234567890123456789
      - run: go test ./...
`))
	if err != nil || validation.Purpose != PurposeValidation || !validation.OrdinaryValidation {
		t.Fatalf("classification=%+v err=%v", validation, err)
	}
	if validation.MutableSnapshotPublish {
		t.Fatal("validation evidence must not imply mutable publication")
	}
}

func TestClassifyWorkflowPurposeDetectsBuildAndDeploymentActions(t *testing.T) {
	build, err := ClassifyWorkflow([]byte(`name: ci
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@0123456789012345678901234567890123456789
      - run: go build ./...
`))
	if err != nil || build.Purpose != PurposeBuild || build.OrdinaryValidation {
		t.Fatalf("build classification=%+v err=%v", build, err)
	}

	publish, err := ClassifyWorkflow([]byte(`name: test
on: [push]
jobs:
  images:
    runs-on: ubuntu-latest
    steps:
      - uses: docker/build-push-action@0123456789012345678901234567890123456789
        with:
          push: true
          tags: ghcr.io/example/app:latest
`))
	if err != nil || publish.Purpose != PurposeDeployment || !publish.MutableSnapshotPublish {
		t.Fatalf("publication classification=%+v err=%v", publish, err)
	}
}

func TestClassifyWorkflowPurposeFailsClosedForUnknownOrMalformedEvidence(t *testing.T) {
	unknown, err := ClassifyWorkflow([]byte(`name: audit
on: [push]
jobs:
  opaque:
    runs-on: ubuntu-latest
    steps:
      - uses: vendor/unknown-action@0123456789012345678901234567890123456789
      - run: go test ./...
`))
	if err != nil || unknown.Purpose != PurposeUnknown || unknown.OrdinaryValidation {
		t.Fatalf("unknown action was certified: %+v err=%v", unknown, err)
	}

	arbitraryScript, err := ClassifyWorkflow([]byte(`name: ci
on: [push]
jobs:
  checks:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@0123456789012345678901234567890123456789
      - uses: actions/github-script@0123456789012345678901234567890123456789
        with:
          script: |
            const result = await fetch("https://example.invalid/mutate", { method: "POST" });
      - run: go test ./...
`))
	if err != nil || arbitraryScript.Purpose != PurposeUnknown || arbitraryScript.OrdinaryValidation {
		t.Fatalf("arbitrary github-script was certified: %+v err=%v", arbitraryScript, err)
	}

	compoundRun, err := ClassifyWorkflow([]byte(`name: ci
on: [push]
jobs:
  checks:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@0123456789012345678901234567890123456789
      - run: go test ./... && curl -X POST https://example.invalid/deploy
`))
	if err != nil || compoundRun.Purpose != PurposeUnknown || compoundRun.OrdinaryValidation {
		t.Fatalf("compound shell command was certified: %+v err=%v", compoundRun, err)
	}

	echoRun, err := ClassifyWorkflow([]byte(`name: ci
on: [push]
jobs:
  checks:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@0123456789012345678901234567890123456789
      - run: echo "go test ./..."
`))
	if err != nil || echoRun.Purpose != PurposeUnknown || echoRun.OrdinaryValidation {
		t.Fatalf("text mentioning a test command was certified: %+v err=%v", echoRun, err)
	}

	if _, err := ClassifyWorkflow([]byte("jobs: [")); err == nil {
		t.Fatal("malformed workflow must fail classification")
	}
}
