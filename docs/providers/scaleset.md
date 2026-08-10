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

## Authentication

The default Docker path uses the same GitHub App private key as the control plane plus the app client ID and installation ID required by the standalone scale-set client. The PEM is mounted as a Compose secret, copied into a mode-0600 runtime path, and the listener drops privileges before starting.

For repository-scoped scale sets, follow GitHub's current Actions Runner Controller guidance for GitHub App permissions: repository Administration read/write and Metadata read-only. Organization-scoped scale sets require the corresponding organization self-hosted-runner permission and should use a deliberately restricted runner group.

## Lifecycle behavior

The adapter restores active RunnerHub workers after listener restart and clamps its configured maximum to the RunnerHub pool's `maxConcurrency` value. Completed jobs remove the corresponding in-memory scale-set worker entry, while RunnerHub independently reconciles provider cleanup.

A remaining alpha acceptance case is a worker that fails before GitHub assigns a job. The RunnerHub worker itself is reconciled and cleaned up, but the scale-set adapter still needs real burst/failure testing to prove that desired-count redelivery always replaces that lost pre-assignment capacity without operator intervention. Keep this gate open until it is exercised against a real scale set.

The `actions/scaleset` client is still public preview, so the adapter remains isolated from the core lifecycle module and direct JIT registration remains available for diagnostics and explicit provisioning.
