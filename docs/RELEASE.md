# Release gating

RunnerHub does not build or publish a deployment image set until a real GitHub JIT runner has completed an acceptance job.

## Required repository secrets

Configure these secrets on the trusted release repository:

- `RUNNERHUB_ACCEPTANCE_GITHUB_APP_ISSUER`: the GitHub App **numeric App ID** used as the JWT issuer by the acceptance control plane. Do not use the `Iv1...` Client ID for this secret.
- `RUNNERHUB_ACCEPTANCE_GITHUB_APP_PRIVATE_KEY`: a private key for that App.

The App must be installed on the RunnerHub repository and have the same repository-level runner administration access required by normal RunnerHub operation. Keep the acceptance App/repository scope narrow.

If either secret is missing, the release workflow fails before release validation or any RunnerHub container image build begins.

## Live JIT gate

For every deployment-facing release, `.github/workflows/publish-images.yml` runs two coordinated jobs:

1. an Ubuntu GitHub-hosted job starts RunnerHub control + Docker agent **from locally built Go binaries**, using the proposed immutable Actions runner image;
2. a second job queues on the unique `runnerhub-release-acceptance` label.

The acceptance host then:

- creates a private repository-scoped pool;
- asks GitHub for a real JIT configuration;
- starts the official pinned Actions runner image through RunnerHub's Docker provider;
- observes runner registration;
- observes the acceptance job start;
- waits for job completion;
- verifies the ephemeral provider worker is deleted;
- verifies the worker record contains registration, start and completion timestamps;
- verifies the exact runner image reference matches the release pin.

Only after both the self-hosted acceptance job and the host-side lifecycle assertion succeed does the release workflow run full release validation. Container image build/publish jobs depend on that validation.

## Runner image upgrades

The default runner image is deliberately immutable:

`ghcr.io/actions/actions-runner:2.337.0@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4`

To advance it:

1. review the upstream Actions runner release and open regressions;
2. update `internal/images.DefaultRunnerImage` and every deployment/template default to the exact new multi-architecture digest;
3. update the release acceptance workflow's proposed image;
4. run the normal test suite;
5. run the live JIT acceptance gate;
6. merge/promote only after that gate succeeds.

Do not replace the digest-pinned default with `:latest` or another moving tag. The exact worker image reference is persisted in RunnerHub worker records for incident reproduction.

## Diagnostics during acceptance

On pre-job Docker startup failures, RunnerHub stores a bounded, redacted startup diagnostic tail with the worker record before cleanup. Workflow output is not retained by RunnerHub after a job starts. The release script may print the worker record and RunnerHub control/agent log tails on failure, but it never prints the App private key or JIT configuration.
