# Architecture

This document records the first implementation boundaries for RunnerHub. It is intentionally narrower than the product roadmap.

## Initial execution flow

```text
GitHub Actions
      |
      v
RunnerHub control plane
  - GitHub App auth
  - runner pool policy
  - scheduler / reconciliation
  - JIT configuration generation
      |
      | authenticated outbound agent channel
      v
RunnerHub agent on a host
  - reports capabilities
  - invokes one provider
  - creates/inspects/deletes worker
      |
      v
ephemeral worker
  - receives only JIT config
  - executes one trusted job
  - is destroyed
```

## Boundary: core

`internal/core` owns provider-independent state and validation:

- `Host`
- `RunnerPool`
- `Worker`
- `GitHubScope`
- resource requests
- isolation modes
- lifecycle transitions

Core code must not import Docker, TrueNAS, Proxmox, cloud SDKs, or GitHub HTTP clients.

## Boundary: provider

`internal/provider.Provider` is the lifecycle contract for infrastructure-specific worker implementations.

A provider is responsible for:

1. reporting capabilities;
2. validating a concrete worker request;
3. creating the execution environment;
4. inspecting it without relying on fuzzy names;
5. stopping it;
6. deleting it idempotently.

The first provider should be generic Docker on Linux. TrueNAS should implement the same contract rather than introducing TrueNAS behavior into the core scheduler.

## Boundary: GitHub runner backend

`internal/githubrunner.Backend` owns runner registration state. The first backend calls GitHub's JIT runner configuration API through a token-source abstraction so GitHub App installation tokens can be supplied without exposing long-lived credentials to workers.

The backend returns an encoded JIT configuration to the control plane. The control plane passes that short-lived value to the selected provider/worker. The worker must not receive the GitHub App private key, installation credential, or a long-lived PAT.

GitHub currently requires JIT configuration requests to include a runner name, runner-group ID and at least one label. Keep that API behind the backend interface because scale-set APIs and semantics may evolve independently of provider code.

## Reconciliation over imperative orchestration

RunnerHub should persist desired state and repeatedly reconcile it against two independent sources of actual state:

- provider/host state;
- GitHub runner state.

A provider reporting a running worker is not sufficient for health. A worker becomes usable only when GitHub reports the exact stored runner identity as online/ready.

Operations must be idempotent. Worker identity should be based on stored IDs and explicit ownership metadata, never name-prefix matching.

## Security decisions for MVP

- Private/trusted repositories only.
- Ephemeral workers by default.
- Control-plane HTTP binds to loopback by default.
- No automatic Docker socket mount.
- No long-lived GitHub credential in a worker.
- No claim that containers isolate hostile workflow code.
- Public/untrusted workflows require a future VM/microVM isolation mode.

## Near-term implementation order

1. GitHub App token provider.
2. Generic Linux agent enrollment and heartbeat protocol.
3. Docker provider with explicit capability checks and no published ports.
4. End-to-end one-job private-repository smoke test.
5. Reconciliation and exact-ID cleanup.
6. Evaluate the GitHub Runner Scale Set Client behind the existing GitHub backend boundary.
7. TrueNAS provider using the same provider contract.
