# GitHub Runner Scale Set adapter

`scaleset/` is intentionally a nested module because GitHub's standalone `actions/scaleset` client has a newer Go requirement than the core RunnerHub module. Production and CI builds currently use Go 1.26 for both modules.

The adapter creates or reuses a GitHub runner scale set, opens the GitHub message session over outbound HTTPS, scales from the authoritative desired runner count, asks GitHub for JIT configs, and posts those configs to RunnerHub's provider-neutral worker API. Job-started/completed events are fed back to the RunnerHub lifecycle state machine with repository, workflow, job and timing metadata.

## Docker deployment

The Docker image has a dedicated `scaleset` target and the Compose file exposes it under the `autoscale` profile. The alpha quickstart uses a repository-scoped pool with a deterministic pool ID and starts the listener only after the control plane and Docker host agent are online.

```bash
bash scripts/init-docker-env.sh
# Fill in GitHub App client ID, installation ID, PEM path and repository URL.
bash scripts/start-docker.sh
```

The default control plane remains loopback-only. The scale-set listener reaches both GitHub and the local RunnerHub API using outbound/local connections, so this path does not require an inbound GitHub webhook or a public RunnerHub port.

The bootstrap does not report success merely because the listener container started. It waits for a `scale-set listener ready` signal emitted only after GitHub authentication, scale-set resolution/creation, RunnerHub pool lookup, active-worker recovery, and GitHub message-session creation have all succeeded.

## Authentication

The default Docker path uses the same GitHub App private key as the control plane plus the app client ID and installation ID required by the standalone scale-set client. The PEM is mounted as a Compose secret, copied into a mode-0600 runtime path, and the listener drops privileges before starting.

For repository-scoped scale sets, follow GitHub's current Actions Runner Controller guidance for GitHub App permissions: repository Administration read/write, Actions read-only, Pull requests read-only, and Metadata read-only. Organization-scoped scale sets require the corresponding organization self-hosted-runner permission and should use a deliberately restricted runner group.

## Shared organization pools

The Repositories page can configure a shared organization pool for selected
private repositories. First install the RunnerHub GitHub App on the
organization with organization self-hosted-runner administration and access to
the repositories that should use the pool. In GitHub, create or select a runner
group whose visibility is **selected repositories**, allow only the intended
private repositories, and disable public repository access. If workflows also
need a GitHub-enforced allowlist, restrict that runner group to the selected
workflow refs.

In RunnerHub, choose the organization, the restricted runner group, the private
repository allowlist, and a scale-set label such as `runnerhub-org`. Use a new
label while migrating from repository pools; RunnerHub rejects a label that
would route the same repository through both a shared and dedicated pool.
Update workflow `runs-on` labels deliberately, verify the shared listener and a
real smoke job, then disable the old pool after its jobs drain. RunnerHub
checks every repository against the signed-in App installation, checks the
group's live visibility and selected repositories, and refuses public or
broader-than-allowlisted access. The listener registers at the organization
URL and uses the verified runner-group name. Pool policy checks the same
repository/workflow allowlists again when a runner is admitted. Disabling the
pool stops its listener configuration; existing jobs can drain.

The `runnerhub` label can be shared by approved trusted repositories. Keep
publisher and hardware-specific pools dedicated when their privilege or
placement requirements differ. Shared organization pools change routing and
do not add physical capacity. They still require a live multi-repository
smoke and migration/rollback acceptance before production reliance.

## Lifecycle behavior

The adapter restores active RunnerHub workers after listener restart and clamps its configured maximum to the RunnerHub pool's `maxConcurrency` value. Completed jobs remove the corresponding in-memory scale-set worker entry, while RunnerHub independently reconciles provider cleanup.

Before every desired-runner-count decision, the adapter re-reads active workers from RunnerHub. This drops locally cached runners that have already failed or been cleaned up and recovers active workers that were not present in the listener's local map. A provider failure before GitHub assigns a job therefore cannot permanently consume the listener's local capacity count.

The adapter checks RunnerHub pool/host capacity **before** asking GitHub for a JIT runner. Capacity exhaustion is backpressure, not a listener failure: queued jobs remain queued and are retried when capacity becomes available. If capacity changes between that preflight and worker creation, RunnerHub removes the just-generated GitHub JIT runner instead of leaking it into the repository runner list. The generated GitHub runner ID is also persisted with the RunnerHub worker so later provider failures can be reconciled and deleted by the control plane.

Real burst, cancellation, listener-restart, and host-loss testing against GitHub remains an acceptance gate. The repository tests prove local state recovery behavior but cannot prove GitHub message redelivery and assignment behavior under every failure ordering.

The `actions/scaleset` client is still public preview, so the adapter remains isolated from the core lifecycle module and direct JIT registration remains available for diagnostics and explicit provisioning.
