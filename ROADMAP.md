# RunnerHub Roadmap

> A control plane for running GitHub Actions safely and simply on compute you already own.

**Status:** product/architecture roadmap  
**Last researched:** 2026-08-08  
**Initial target:** private GitHub repositories on Linux/Docker and TrueNAS hosts

---

## 1. Product thesis

GitHub self-hosted runners are easy to install once and surprisingly hard to operate well.

The difficult parts are not downloading the runner binary. They are:

- securely connecting GitHub without leaving long-lived credentials on workers;
- provisioning and destroying runners reliably;
- keeping runner versions and images current;
- handling stale registrations and failed deployments;
- matching jobs to the right hardware;
- preventing one job from contaminating the next;
- controlling Docker/privileged capabilities;
- tracking which machines are online, healthy, busy, or drifting;
- scaling beyond one hand-managed machine;
- giving teams a usable view of capacity, failures, and cost;
- doing all of this without requiring Kubernetes.

RunnerHub should make those operational problems disappear.

The long-term user experience should be approximately:

1. Install the RunnerHub agent on a machine.
2. Install the RunnerHub GitHub App.
3. The machine appears in the RunnerHub dashboard.
4. Create a runner pool such as `linux-small`, `docker-build`, `gpu`, or `publisher`.
5. Select repositories/workflows allowed to use the pool.
6. RunnerHub creates clean ephemeral workers, routes work, reconciles state, updates images, collects diagnostics, and removes stale registrations.
7. The workflow only needs a stable `runs-on` target.

RunnerHub is **not** intended to replace GitHub Actions. It is the control plane between GitHub Actions and user-owned compute.

---

## 2. Why this is worth building

GitHub explicitly supports self-hosted runners on physical machines, virtual machines, containers, on-premises systems, and cloud infrastructure. GitHub also calls out existing infrastructure, custom hardware, local-network access, and custom resource requirements as reasons to self-host.

In February 2026, GitHub released the open-source **Actions Runner Scale Set Client** in public preview. It provides GitHub-side scale-set orchestration without requiring Kubernetes and deliberately leaves infrastructure provisioning to integrators. This materially improves the foundation for RunnerHub: we should use GitHub's scale-set/JIT primitives rather than inventing our own queue protocol.

GitHub still leaves provisioning, isolation, capacity management, host lifecycle, images, observability, and infrastructure automation to the operator. That is RunnerHub's opportunity.

### Market validation

There are already successful products around GitHub Actions runner infrastructure:

| Product | Primary model | What it validates | Gap relevant to RunnerHub |
|---|---|---|---|
| GitHub ARC | Kubernetes-based autoscaling | Organizations need managed ephemeral runner fleets | Requires Kubernetes and platform expertise |
| GitHub Scale Set Client | SDK/primitives for custom autoscaling | GitHub wants non-Kubernetes infrastructure integrations | It does not provision or manage infrastructure for the user |
| RunsOn | Ephemeral runners in the customer's AWS account | Customers will pay for easier BYOC runners | AWS-focused; optimized for cloud rather than miscellaneous owned hardware |
| Cirun | BYOC across many clouds plus on-prem pools | Multi-provider/BYO infrastructure is a real market | Broad competitor; RunnerHub must be substantially easier for owned/local hardware |
| Actuated | Managed Firecracker runners on customer servers | Teams value using their own hardware with strong isolation | Strong closest competitor for secure bare-metal CI |
| WarpBuild | Hosted and BYOC runners on AWS/GCP/Azure | Performance, caching, BYOC, and observability are monetizable | Cloud-centric and enterprise/performance focused |

This is **not an empty market**. That is useful validation, but it means "easy self-hosted runners" is not enough differentiation.

### Initial wedge

RunnerHub should start with:

> **The easiest way for a small team to turn existing Linux servers, NAS boxes, mini PCs, workstations, and specialized hardware into a reliable GitHub Actions runner fleet.**

Initial differentiation:

- generic existing hardware, not only AWS/GCP/Azure;
- no Kubernetes requirement;
- outbound-only host agent;
- clean ephemeral runners by default;
- strong TrueNAS support as the first appliance integration;
- simple pools rather than individually managed runner registrations;
- GitHub App + JIT credentials rather than PATs on worker containers;
- health reconciliation across both GitHub and the physical host;
- gradual path from trusted-container mode to microVM isolation;
- useful at one server, but capable of growing into a fleet;
- self-hostable/local control-plane option should remain possible.

Do not try to beat RunsOn/WarpBuild on cloud instance pricing in the first product. Do not try to beat ARC at Kubernetes. Win on **owned heterogeneous compute + simplicity**.

---

## 3. Product principles

### 3.1 Pools, not individual runners

The main user abstraction should be a **runner pool**.

Examples:

```text
linux-small
  hosts: lab-1, lab-2
  cpu/job: 4
  memory/job: 8 GiB
  concurrency: 4
  ephemeral: true
  labels: [linux-small]

docker-build
  hosts: buildbox
  cpu/job: 8
  memory/job: 16 GiB
  docker: isolated
  concurrency: 2
  labels: [docker-build]

gpu
  hosts: gpu-box
  gpu/job: 1
  concurrency: 1
  labels: [gpu, cuda]
```

The individual GitHub runner registration is an implementation detail.

### 3.2 Ephemeral by default

GitHub recommends ephemeral runners for autoscaling. RunnerHub should treat one-job workers as the normal execution model.

Persistent runners should exist only as an explicit compatibility profile for trusted jobs that genuinely require them.

### 3.3 No long-lived GitHub credential on a worker

The control plane should hold GitHub App credentials. A worker should receive only a short-lived JIT configuration/registration credential for the job it is about to execute.

Do not make a PAT mount into every runner part of the final architecture.

### 3.4 Outbound-only agent

A host should not need a public port opened for RunnerHub.

The host agent should establish an authenticated outbound connection to the control plane and receive desired-state commands over that channel. GitHub runner traffic itself is outbound to GitHub.

### 3.5 Security capability is explicit

A job capable of using Docker, host mounts, devices, GPUs, KVM, or privileged execution is meaningfully more powerful than an ordinary job.

RunnerHub must model those capabilities explicitly and make policies visible.

Never silently mount `/var/run/docker.sock` because a workflow happens to need Docker.

### 3.6 Host state and GitHub state are separate

`container = running` does not mean `runner = healthy`.

RunnerHub should reconcile:

- host/agent connectivity;
- provisioner state;
- worker/container/VM state;
- GitHub runner registration;
- GitHub `online`/`busy` state;
- expected labels/pool;
- runner version;
- last job and lifecycle result.

### 3.7 Provider-specific code stays behind adapters

TrueNAS is the first supported appliance, not the architecture.

Core logic must not know how TrueNAS, Docker, Proxmox, or a cloud creates a worker.

---

## 4. Proposed architecture

```text
                              GitHub
                                |
                      GitHub App / Scale Sets
                                |
                                v
+----------------------------------------------------------------+
|                       RunnerHub Control Plane                  |
|                                                                |
|  API / Web UI                                                  |
|  GitHub adapter                                                |
|  Scale-set controller                                         |
|  Pool scheduler                                                |
|  Host registry                                                 |
|  Desired-state / reconciliation engine                        |
|  Policy engine                                                 |
|  Audit / metrics / diagnostics                                |
|  Secret store                                                  |
+----------------------------+-----------------------------------+
                             |
                  outbound authenticated channel
                             |
          +------------------+-------------------+
          |                                      |
          v                                      v
+--------------------+                  +--------------------+
| RunnerHub Agent    |                  | RunnerHub Agent    |
| host: TrueNAS      |                  | host: Linux        |
|                    |                  |                    |
| provider adapter   |                  | provider adapter   |
| capability report  |                  | capability report  |
| lifecycle executor |                  | lifecycle executor |
+---------+----------+                  +---------+----------+
          |                                      |
   +------+------+                         +-----+------+
   | worker      |                         | worker     |
   | ephemeral   |                         | ephemeral  |
   +-------------+                         +------------+
```

### Core domains

#### `Host`
A physical/virtual machine running the RunnerHub agent.

Suggested fields:

- ID and display name
- provider type
- OS and architecture
- CPU / RAM / disk
- GPU/device inventory
- virtualization support (`kvm`, nested virtualization)
- Docker/container runtime capabilities
- agent version
- connectivity/heartbeat
- tags
- drain state
- current capacity

#### `RunnerPool`
Stable execution target used by workflows.

Suggested fields:

- pool ID/name
- GitHub scope
- labels
- allowed repositories/workflows
- compatible hosts/tags
- worker profile
- isolation mode
- resource request
- min/max/warm capacity
- concurrency
- lifecycle (`ephemeral` default)
- image/template
- timeout policies

#### `Worker`
One concrete runner execution environment.

Suggested fields:

- worker ID
- host ID
- pool ID
- GitHub runner/scale-set ID
- runtime name
- job ID if assigned
- state
- creation/start/completion timestamps
- image/version
- safe failure reason

#### `Provider`
Infrastructure-specific worker lifecycle interface.

Initial shape:

```go
type Provider interface {
    Capabilities(ctx context.Context) (Capabilities, error)
    Validate(ctx context.Context, spec WorkerSpec) error
    Create(ctx context.Context, spec WorkerSpec) (WorkerHandle, error)
    Inspect(ctx context.Context, handle WorkerHandle) (WorkerState, error)
    Stop(ctx context.Context, handle WorkerHandle) error
    Delete(ctx context.Context, handle WorkerHandle) error
}
```

Provider candidates, in priority order:

1. Docker on generic Linux
2. TrueNAS
3. local process/systemd compatibility mode
4. Proxmox
5. Firecracker/KVM worker provider
6. cloud VM providers only when customer demand justifies them

---

## 5. Authentication and GitHub integration

### Default: GitHub App

The normal product onboarding should be a GitHub App installation, not asking users to paste a PAT.

Required capabilities need to be kept as narrow as GitHub permits. Repository-scoped management can use repository Administration permissions for runner APIs; organization runner management uses the Self-hosted runners organization permission.

### Compatibility: fine-grained PAT

Fine-grained PAT support is still useful for:

- local-only/self-hosted deployments;
- early development;
- users unable to install the GitHub App.

PATs should be a fallback, not the primary UX.

### JIT runner configuration

Use GitHub's JIT runner configuration endpoint or Scale Set Client whenever possible.

The control plane generates the JIT configuration and sends only the short-lived encoded configuration to the selected host. Long-lived GitHub App/PAT credentials never reach the worker.

### Scale Set Client

Evaluate `actions/scaleset` as the default GitHub-side orchestration foundation before building a webhook-driven scheduler.

The client is specifically intended for custom autoscaling on containers, VMs, bare metal, and cloud infrastructure without requiring Kubernetes. RunnerHub should provide the infrastructure provisioning/control layer that the client intentionally leaves to integrators.

Do not tightly couple the product to a preview API without an abstraction boundary. Keep a `GitHubRunnerBackend` interface so JIT runner APIs can remain a fallback.

---

## 6. Security model

Security is a product feature, not a hardening phase added at the end.

### Threat model

Assume workflow code is arbitrary code with the privileges of its execution environment.

GitHub warns that ordinary self-hosted runners can be persistently compromised by untrusted workflow code and recommends extreme caution, especially for public repositories/forks.

Therefore RunnerHub v1 should explicitly support **trusted private-repository workloads only**.

### Isolation tiers

Expose isolation as a first-class property:

| Tier | Isolation | Intended use | Initial status |
|---|---|---|---|
| `container-trusted` | disposable container | trusted private repo CI | MVP |
| `host-persistent` | persistent host/service runner | narrowly trusted publisher/special hardware | compatibility only |
| `microvm` | Firecracker/KVM VM per job | stronger isolation / broader workloads | post-MVP priority |
| `vm-provider` | provider-created VM per job | Proxmox/cloud/etc. | later |

Do **not** claim a container alone is a security boundary equivalent to a fresh VM.

### Docker builds

Three eventual execution paths:

1. No Docker capability.
2. Docker inside an isolated VM/microVM — preferred secure path.
3. Host Docker socket — compatibility path with explicit warning and restricted use.

Host Docker socket mode should be unavailable to untrusted/public workloads.

### Secrets

- Encrypt control-plane secrets at rest.
- Never return secret values after creation.
- Never put GitHub App keys, PATs, registration tokens, JIT config, Authorization headers, or host enrollment secrets in logs.
- Redact command/environment diagnostics.
- Rotate host credentials.
- Use per-host identity rather than one global agent secret.

### Host/control-plane transport

Target:

- TLS for all traffic;
- per-agent identity;
- short-lived enrollment token used only to bootstrap identity;
- certificate/key rotation;
- outbound-only persistent connection;
- command signing or authenticated message stream;
- nonce/idempotency key on lifecycle operations.

### Runner version management

GitHub resumed minimum-version enforcement for self-hosted runners in 2026. RunnerHub therefore needs a real update/image policy:

- track installed runner version;
- know latest supported/pinned version;
- alert before enforcement problems;
- drain obsolete workers;
- roll updated worker images/templates;
- never rely on forgotten static images indefinitely.

---

## 7. Reconciliation and state machine

RunnerHub should be reconciliation-driven rather than a collection of imperative shell commands.

### Desired states

Host:

```text
enrolling -> online -> draining -> offline
                    -> error
```

Worker:

```text
requested
  -> provisioning
  -> starting
  -> registering
  -> ready
  -> busy
  -> completed
  -> deleting
  -> deleted
```

Failure paths:

```text
provisioning/registering/ready/busy
  -> failed
  -> cleanup_pending
  -> deleted
```

### Reconciliation loop

For every active worker/pool:

1. Inspect host availability.
2. Inspect provider worker state.
3. Inspect GitHub runner/scale-set state.
4. Compare actual state to desired state.
5. Repair safe drift.
6. Mark unsafe/ambiguous drift for operator action.
7. Record a redacted audit event.

Never delete a resource based only on a prefix or fuzzy name match. Use stored IDs and exact ownership metadata.

### Idempotency

Every create/delete operation should be retry-safe.

A control-plane restart in the middle of provisioning must not create duplicate runners or delete another instance.

---

## 8. Observability

RunnerHub should answer these without SSHing into a box:

- Why is my job queued?
- Which host could accept it?
- Why did worker creation fail?
- Did GitHub ever see the runner online?
- Is the runner busy or stuck?
- Which image and runner version ran the job?
- How long did provisioning take?
- How much host capacity is currently available?
- Did the runner clean itself up?
- Is there a stale GitHub registration?

### Metrics to capture

- runner requests
- queue-to-provision latency
- provision duration
- registration duration
- ready-to-job latency
- job duration
- cleanup duration
- lifecycle success/failure rate
- active/idle/busy workers
- host CPU/RAM/disk pressure
- per-pool concurrency
- stale registrations
- agent heartbeat age
- runner version distribution

GitHub recommends preserving external runner logs for ephemeral runner troubleshooting. RunnerHub should collect safe agent/runner diagnostics centrally while ensuring workflow secrets are not accidentally retained outside GitHub.

---

# Implementation roadmap

## Phase 0 — Research spike and architecture lock

**Goal:** avoid baking the original TrueNAS prototype assumptions into the core product.

### Work

- [ ] Prototype GitHub App authentication.
- [ ] Prototype repository and organization JIT runner configuration.
- [ ] Prototype `actions/scaleset` against a tiny local provisioner.
- [ ] Decide whether the first production backend uses Scale Set Client, direct JIT APIs, or both behind one interface.
- [ ] Define `Host`, `RunnerPool`, `Worker`, `Provider`, and `GitHubRunnerBackend` interfaces.
- [ ] Define worker lifecycle state machine and idempotency model.
- [ ] Define threat model and document v1 trusted-workload boundary.
- [ ] Decide control-plane implementation language and persistence stack.
- [ ] Preserve useful logic from the existing TrueNAS runner-manager design, but move it behind the new provider model.

### Exit criteria

- One throwaway worker can be created from a GitHub job request without placing a PAT on the worker.
- Worker is cleaned up and GitHub has no stale runner record.
- Architecture supports both Docker and TrueNAS providers without core conditionals such as `if truenas`.

---

## Phase 1 — Deterministic core + Docker dogfood

**Goal:** execute trusted private-repository jobs as clean disposable containers on one Linux host.

### Control plane

- [ ] GitHub App installation flow.
- [ ] Repository discovery.
- [ ] SQLite for single-node development; storage interface designed so Postgres can replace it.
- [ ] Host registry.
- [ ] Runner pool model.
- [ ] Worker lifecycle state machine.
- [ ] Audit event model.
- [ ] Reconciliation engine.
- [ ] Safe structured error model.

### Agent

- [ ] Linux agent daemon.
- [ ] One-time enrollment flow.
- [ ] Outbound authenticated control-plane connection.
- [ ] Heartbeats and capability inventory.
- [ ] Docker provider.
- [ ] Image allowlist.
- [ ] CPU/memory/workdir limits.
- [ ] No published worker ports by default.
- [ ] No Docker socket in default profile.

### GitHub

- [ ] JIT/ephemeral registration.
- [ ] Exact GitHub runner ID/name tracking.
- [ ] Stable pool label or scale-set name.
- [ ] Smoke workflow generator/test fixture.
- [ ] Reconcile `online`, `busy`, labels, and scope.

### Tests

- [ ] Validation/unit tests.
- [ ] Mock GitHub API tests.
- [ ] Provider lifecycle tests.
- [ ] Crash/restart idempotency tests.
- [ ] Secret-redaction tests.
- [ ] 100-job lifecycle soak with zero stale registrations.

### Exit criteria

A new user can install the agent on Ubuntu, connect one private repository, create a pool, run 100 ephemeral smoke jobs, and end with zero stale workers/runners and no GitHub credential exposed to worker containers.

---

## Phase 2 — TrueNAS integration + usable UI

**Goal:** turn the original TrueNAS use case into the first polished appliance integration.

### TrueNAS provider

- [ ] TrueNAS capability discovery.
- [ ] Narrow TrueNAS API/local-agent adapter.
- [ ] Structured custom-app creation.
- [ ] Deterministic generated app names/paths.
- [ ] App task tracking.
- [ ] Container state inspection.
- [ ] Safe cleanup.
- [ ] No arbitrary Compose YAML from the UI.
- [ ] Enforce host path root.
- [ ] Explicit Docker capability profile.

### UI

- [ ] GitHub connection page.
- [ ] Hosts page.
- [ ] Host details/capabilities.
- [ ] Runner pools page.
- [ ] Create pool wizard.
- [ ] Repository/workflow access selection.
- [ ] Active workers page.
- [ ] Separate host/provider/GitHub health indicators.
- [ ] Safe event timeline.
- [ ] Drain/restart/delete controls.
- [ ] Smoke test button.

### Migration/dogfood

- [ ] Support importing or replacing existing hand-created self-hosted runners.
- [ ] Migrate at least two real repositories to RunnerHub pools.
- [ ] Run RunnerHub's own CI on RunnerHub.

### Exit criteria

The entire existing TrueNAS runner workflow can be reproduced from RunnerHub without manually creating a GitHub runner, Docker Compose app, credential file, or cleanup script.

---

## Phase 3 — Multi-host fleets

**Goal:** make RunnerHub valuable beyond one NAS.

### Scheduling

- [ ] Pool-to-host matching by capabilities/tags.
- [ ] CPU/RAM capacity accounting.
- [ ] Concurrency limits.
- [ ] Host draining.
- [ ] Offline host avoidance.
- [ ] Scheduling reason surfaced in UI.
- [ ] Prefer-local / pin-host policies.
- [ ] GPU/device matching model.

### Fleet operations

- [ ] Agent auto-update mechanism.
- [ ] Host rename/tags.
- [ ] Rolling worker image updates.
- [ ] Fleet runner-version dashboard.
- [ ] Pool-level health.
- [ ] Bulk drain/update.

### Exit criteria

A pool can span 3+ heterogeneous hosts, survive one host going offline, and continue scheduling compatible jobs without manual GitHub runner changes.

---

## Phase 4 — Native scale sets and autoscaling

**Goal:** adopt GitHub's current scale-set path for reliable queue-driven ephemeral provisioning.

### Work

- [ ] Production integration with `actions/scaleset` if Phase 0 validation holds.
- [ ] Per-pool scale set lifecycle.
- [ ] Job-demand-driven worker creation.
- [ ] Scale-to-zero.
- [ ] Min/max capacity.
- [ ] Optional warm capacity.
- [ ] Burst controls.
- [ ] Backpressure when host capacity is exhausted.
- [ ] Cancellation cleanup.
- [ ] Control-plane restart recovery.
- [ ] Real-time scheduling metrics.

### Exit criteria

RunnerHub can receive bursty queued work, create the required ephemeral workers, respect capacity limits, and return to zero idle workers without webhook polling hacks or stale state.

---

## Phase 5 — Strong isolation

**Goal:** make untrusted jobs substantially safer and remove the need for host Docker socket access in normal Docker-build workflows.

### Firecracker/KVM provider

- [ ] Detect KVM/nested virtualization.
- [ ] Minimal runner VM image pipeline.
- [ ] Firecracker or equivalent microVM launcher.
- [ ] Per-job ephemeral root filesystem.
- [ ] Resource limits.
- [ ] Network policy hooks.
- [ ] Device/GPU pass-through research and guarded support.
- [ ] Native Docker engine inside worker VM.
- [ ] VM teardown verification.

### Image supply chain

- [ ] Signed/pinned base images.
- [ ] SBOM generation.
- [ ] Image version inventory.
- [ ] Controlled rollout/rollback.
- [ ] Automated runner-version refresh.

### Exit criteria

A Docker-building workflow can run in a disposable VM/microVM without mounting the host Docker socket, and teardown leaves no writable job filesystem for the next job.

**Only after this phase should public/untrusted repository support be considered.** It requires a separate security review and should not be enabled merely because microVMs exist.

---

## Phase 6 — Organization-grade controls

**Goal:** support small engineering organizations rather than only individual repositories.

### GitHub scope

- [ ] Organization installations.
- [ ] Organization runner groups.
- [ ] Repository access policies.
- [ ] Workflow access policies.
- [ ] Multiple labels/capability routing where supported.
- [ ] Organization-wide usage view.

### RunnerHub authorization

- [ ] Multiple users.
- [ ] RBAC.
- [ ] Admin/operator/viewer roles.
- [ ] SSO/OIDC.
- [ ] Immutable audit trail/export.

### Policy

- [ ] Deny public repositories by default.
- [ ] Deny privileged capabilities by default.
- [ ] Per-pool repository/workflow allowlists.
- [ ] Image allowlists.
- [ ] Maximum resources and concurrency.
- [ ] Network policy profiles.
- [ ] Required isolation tier per repository/pool.

### Exit criteria

An organization administrator can safely delegate runner use without giving every repository access to every host or privileged capability.

---

## Phase 7 — Additional providers

**Goal:** expand only where the same control-plane UX provides real user value.

### Priority order

#### Proxmox

Strong fit with the existing-hardware thesis.

- [ ] VM template provider.
- [ ] clone/start/stop/delete lifecycle.
- [ ] ephemeral disk handling.
- [ ] cloud-init/JIT bootstrap.

#### Generic VM/libvirt

- [ ] local KVM/libvirt provider where Firecracker is not suitable.

#### macOS

- [ ] persistent Mac host support first.
- [ ] evaluate VM isolation constraints separately.

#### Windows

- [ ] Windows agent.
- [ ] Windows worker lifecycle.

#### Cloud providers

AWS/GCP/Azure are intentionally later. RunsOn, Cirun, WarpBuild, ARC, and many internal tools already serve this space well.

Add cloud providers when users specifically want one RunnerHub control plane spanning their owned hosts and cloud overflow capacity.

Potential later capability:

- [ ] overflow from local pool to cloud provider when all local hosts are busy.

---

## Phase 8 — Performance platform features

**Goal:** move from "runner management" toward "CI infrastructure platform" only after the core is reliable.

Possible features:

- [ ] shared/local dependency cache;
- [ ] GitHub Actions cache proxy/backend;
- [ ] Docker layer cache;
- [ ] image prewarming;
- [ ] workspace snapshots where isolation model permits;
- [ ] per-job provisioning/runtime breakdown;
- [ ] energy/cost accounting for owned hardware;
- [ ] cloud-vs-local cost estimates;
- [ ] queue analytics;
- [ ] recommended pool sizing;
- [ ] automatic idle-host sleep/wake where hardware supports it.

Do not build custom caching before runner lifecycle reliability is excellent.

---

## Phase 9 — Commercialization

**Goal:** validate willingness to pay before building broad enterprise surface area.

### Likely packaging

#### Community / self-hosted

Potential scope:

- one control plane;
- limited hosts/concurrency;
- Docker + TrueNAS providers;
- private repositories;
- core dashboard;
- community support.

#### Hosted control plane

RunnerHub hosts only the control plane. Compute remains in the customer's environment behind outbound agents.

Potential paid value:

- more hosts/concurrency;
- team/RBAC;
- organization runner groups;
- richer retention/analytics;
- policy engine;
- microVM management;
- SSO;
- alerting;
- backups;
- enterprise support/SLA.

### Pricing hypothesis to validate

Prefer pricing that aligns with management value rather than charging a large markup on compute the user already owns.

Candidates to test:

- per active host;
- per managed concurrency slot;
- team plan with included hosts/concurrency;
- enterprise flat/annual contract.

Avoid committing to per-minute pricing until interviews show customers prefer it.

### Validation gate

Before building broad cloud-provider support or enterprise compliance work:

- interview 10–20 teams currently using or considering self-hosted GitHub runners;
- onboard at least 5 external design partners;
- get at least 3 users to run meaningful CI through RunnerHub for several weeks;
- obtain paid pilots or strong purchase intent from at least 2–3 teams.

---

# MVP definition

RunnerHub MVP is complete when a user can:

1. Install a RunnerHub agent on an Ubuntu/Debian machine or supported TrueNAS host.
2. Install/connect the RunnerHub GitHub App.
3. See the host and its capabilities in the dashboard.
4. Create a `container-trusted` runner pool for a private repository.
5. See the exact `runs-on` value to use.
6. Trigger a workflow.
7. Have RunnerHub create a clean ephemeral worker using a JIT credential.
8. See GitHub report the worker online and busy.
9. See lifecycle and health state in RunnerHub.
10. Have the worker destroyed after the job.
11. Confirm no stale GitHub runner remains.
12. Run 100+ consecutive jobs without manual cleanup.
13. Drain a host without breaking pool configuration.
14. Update the runner image/version without hand-editing every runner.
15. Inspect useful diagnostics without exposing GitHub credentials.

### Explicit MVP non-goals

- public repository runners;
- arbitrary Compose/YAML execution supplied through the UI;
- Kubernetes/ARC replacement;
- AWS/GCP/Azure marketplace;
- enterprise SSO;
- billing;
- cross-CI support;
- perfect isolation for hostile code;
- automatic host Docker-socket mounting;
- giant workflow analytics product.

---

# Recommended repository layout

A Go control-plane/agent is particularly attractive because GitHub's Scale Set Client is Go-based, but implementation language should be locked only after the Phase 0 spike.

Potential layout:

```text
runnerhub/
  cmd/
    runnerhub/
    runnerhub-agent/
  internal/
    api/
    auth/
    github/
    scheduler/
    reconcile/
    policy/
    telemetry/
    store/
    agent/
    providers/
      docker/
      truenas/
      firecracker/
      proxmox/
  web/
  migrations/
  deploy/
    docker/
    truenas/
  docs/
    architecture/
    security/
    providers/
  tests/
    integration/
    soak/
  ROADMAP.md
  README.md
```

If the web application is implemented separately in TypeScript/Next.js, keep the control-plane contract explicit rather than sharing infrastructure-specific logic through the frontend.

---

# Near-term build order

If development starts immediately, use this order:

1. **Spike `actions/scaleset` and GitHub JIT config.**
2. **Define the provider + worker state-machine interfaces.**
3. **Create the Linux agent with outbound enrollment/heartbeat.**
4. **Implement Docker ephemeral worker lifecycle.**
5. **Implement GitHub App installation and repo discovery.**
6. **Wire one private repo end to end.**
7. **Add reconciliation and safe cleanup.**
8. **Run a 100-job soak test.**
9. **Add the TrueNAS provider.**
10. **Build the smallest UI needed to create/view pools and hosts.**
11. **Dogfood multiple repositories.**
12. **Only then add multi-host scheduling/autoscaling.**

The most important milestone is not a polished dashboard. It is proving that RunnerHub can repeatedly take a queued job, create the correct isolated-enough worker, run it, delete it, and recover from failures with **zero manual runner cleanup**.

---

# Product success metrics

Early technical metrics:

- first runner online in <10 minutes from a fresh supported host;
- >99% worker lifecycle success in soak testing;
- zero stale GitHub registrations after normal successful jobs;
- deterministic cleanup after control-plane/agent restarts;
- no long-lived GitHub credential inside job workers;
- no published runner ports by default;
- pool survives loss of one host once multi-host scheduling ships;
- runner version compliance visible before GitHub enforcement causes failures.

Early product metrics:

- time to first successful job;
- percentage of installs that reach first job;
- active hosts per installation;
- repositories/pools per installation;
- weekly CI jobs executed;
- manual-intervention rate;
- external users still running RunnerHub after 30 days;
- conversion from design partner to paid pilot.

---

# Major risks

## 1. The market is already competitive

Cirun and Actuated overlap materially with the concept. RunnerHub cannot rely on "self-hosted runners made easy" as a moat.

**Response:** focus on the easiest small-team/on-prem heterogeneous hardware experience, self-hostability, appliance/provider integrations, and incremental adoption.

## 2. Containers are not sufficient isolation for hostile CI

**Response:** restrict MVP to trusted private workloads, make privilege explicit, and prioritize microVM isolation before broadening trust boundaries.

## 3. GitHub's Scale Set Client is still preview-era technology

**Response:** isolate it behind `GitHubRunnerBackend`, retain direct JIT support, and track API/client changes.

## 4. Too many providers can kill execution speed

**Response:** Docker + TrueNAS first, Proxmox next. Do not build every cloud provider before user demand.

## 5. Control-plane compromise is high impact

**Response:** minimize GitHub App permissions, isolate secrets, use short-lived worker credentials, authenticated agents, audit logs, and a documented threat model from the first release.

## 6. A runner manager can become an entire CI platform

**Response:** keep the boundary clear. RunnerHub owns runner infrastructure lifecycle. GitHub owns workflow definitions, job logs, checks, and CI semantics unless there is a compelling reason to augment them.

---

# Research sources

Primary/current sources used for this roadmap:

- GitHub: Self-hosted runners reference — https://docs.github.com/en/actions/reference/runners/self-hosted-runners
- GitHub: Actions Runner Controller — https://docs.github.com/en/actions/concepts/runners/actions-runner-controller
- GitHub: Runner scale sets — https://docs.github.com/en/actions/concepts/runners/runner-scale-sets
- GitHub: Self-hosted runner REST API / JIT configuration — https://docs.github.com/en/rest/actions/self-hosted-runners
- GitHub: Runner groups — https://docs.github.com/en/actions/concepts/runners/runner-groups
- GitHub: Secure use reference — https://docs.github.com/en/actions/reference/security/secure-use
- GitHub: Compromised runners — https://docs.github.com/en/actions/concepts/security/compromised-runners
- GitHub changelog: Runner Scale Set Client public preview, Feb. 2026 — https://github.blog/changelog/2026-02-05-github-actions-early-february-2026-updates/
- GitHub changelog: self-hosted runner minimum-version enforcement, June 2026 — https://github.blog/changelog/2026-06-12-github-actions-minimum-version-enforcement-timeline-for-self-hosted-runners/
- RunsOn documentation — https://runs-on.com/docs/
- Cirun documentation — https://docs.cirun.io/
- Actuated documentation — https://docs.actuated.com/
- WarpBuild BYOC documentation — https://www.warpbuild.com/docs/ci/byoc

---

## Bottom line

Build RunnerHub as a **runner infrastructure control plane**, not a TrueNAS runner GUI and not another hosted runner vendor.

TrueNAS is an excellent dogfood provider. Generic Linux/Docker makes the product broadly usable. GitHub's Scale Set Client gives the project a strong modern orchestration foundation. MicroVM isolation is the path to stronger security. The product wins only if connecting existing hardware and operating it as a reliable runner fleet becomes dramatically simpler than doing it manually.
