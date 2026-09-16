# Security model

RunnerHub treats workflow code as arbitrary code with the privileges of its worker environment.

## Supported trust tiers

- `container-trusted`: disposable Docker/TrueNAS worker for trusted private repositories. No published ports, no host Docker socket, `no-new-privileges`, and no privileged sidecar. Docker container actions, service containers, and `container:` jobs are not supported in this mode.
- `container-dind`: compatibility mode for trusted private repositories that require Docker container actions, service containers, or `container:` jobs. Each worker gets a private Docker-in-Docker daemon instead of the host Docker socket. The DinD sidecar is privileged, so this mode is a weaker host-isolation boundary and is disabled unless `RUNNERHUB_ALLOW_PRIVILEGED_WORKERS=true` is explicitly configured.
- `host-persistent`: compatibility mode for trusted special hardware, macOS, or Windows using the process provider.
- `microvm`: Firecracker/KVM worker. The provider creates a fresh writable rootfs, per-worker TAP interface, and separate read-only seed disk containing only the short-lived JIT configuration.
- `vm-provider`: provider-created VM, currently Proxmox, bootstrapped with a generated cloud-init snippet containing only the JIT configuration.

Public/untrusted workflows are denied by default and should remain disabled until the microVM/VM path has received a dedicated live security review. `container-dind` is not an untrusted-code sandbox merely because its Docker daemon is per-worker.

## GitHub assignment boundary

A RunnerHub pool is not considered safe merely because its local configuration contains an allowlist. GitHub runner groups are the assignment boundary for organization-scoped runners. RunnerHub inspects the actual GitHub runner-group visibility, public-repository setting, selected repositories, and workflow restrictions before creating or using a pool.

Repository-scoped pools may be used for trusted private repositories. RunnerHub does not claim that a repository-scoped local workflow allowlist is enforceable at GitHub's runner assignment boundary; use an organization pool with a workflow-restricted runner group when workflow-specific access control is required.

## Credentials

Long-lived GitHub App credentials remain in trusted control components; they are never delivered to job workers. In the default automatic Docker deployment, both `runnerhubd` and the scale-set listener need GitHub App authentication. The App PEM is mounted read-only, copied to an owner-only runtime path, and both processes drop root privileges before normal operation. Workers receive only short-lived JIT configuration.

On the TrueNAS appliance path, RunnerHub's enrollment token, agent secret, admin API key, and scale-set API key are generated once into the dedicated secrets dataset. Services consume them through file-backed `*_FILE` inputs, so those values do not need to be embedded in Custom App Compose YAML. Existing non-empty secret files are preserved across restart/update; empty secret files fail closed rather than silently rotating credentials.

The scale-set listener uses a separate RunnerHub `scaler` API credential rather than the local admin key. That role is restricted to reading pool/worker state and posting scale-worker requests and scale lifecycle events; it cannot read hosts/audit data, drain hosts, modify pools, access cache mutation endpoints, or request direct-JIT workers.

The global enrollment token is a bootstrap credential. After first enrollment, the agent persists only its rotating per-host token in an owner-only credential file and can restart without the enrollment token. Per-host tokens expire and are refreshed during normal heartbeats.

Durable create commands must briefly retain JIT material while an agent is offline. RunnerHub encrypts those command payloads with AEAD using a key derived from the control-plane agent secret, decrypts them only for delivery to the authenticated host, scrubs the stored payload after acknowledgment, and prunes old acknowledged rows.

API keys are hashed in memory before comparison. Trusted identity headers are accepted only from a loopback reverse proxy. A control plane bound beyond loopback requires native TLS unless the operator deliberately enables the insecure-HTTP override for a separately protected network.

## Docker privilege boundary

The trusted Docker host agent mounts `/var/run/docker.sock` because it creates and removes ephemeral workers. The agent is therefore root-equivalent trusted infrastructure. The host socket is never mounted directly into workflow runner containers.

`container-trusted` is the safer Docker mode. Workers publish no ports, run with `no-new-privileges`, receive explicit CPU/memory/workdir limits, and have no Docker daemon available.

`container-dind` exists only for GitHub workflow features that require Docker. RunnerHub creates a dedicated Docker daemon sidecar for that worker and shares only a private Unix-socket directory, work directory, and runner externals with the workflow runner. The sidecar uses Docker `--privileged`; Docker documents privileged containers as highly elevated, so this is not equivalent to a VM or microVM boundary. The control-plane policy rejects a DinD pool unless privileged workers have been explicitly enabled by the administrator.

DinD currently has coarser resource accounting than `container-trusted`: pool CPU/memory limits apply to the runner container, but the privileged DinD sidecar and its nested containers are not yet placed under one scheduler-accounted shared resource envelope, and DinD work/daemon storage is bind-backed rather than using the trusted mode's bounded tmpfs. Pool concurrency and host capacity remain coarse bounds, but strict per-worker DinD CPU, memory, and disk accounting is a hardening item. Do not use DinD for untrusted or adversarial workloads.

The default GitHub Actions runner image is pinned to an immutable multi-architecture digest and production policy requires digest-pinned runner images. Pulling the configured digest before startup is safe because the identity cannot move. Runner image upgrades are deliberate release changes and must pass the live JIT acceptance gate before the deployment-facing image set is promoted. The DinD infrastructure image is independently configurable and should also be pinned/managed deliberately in production.

Firecracker and Proxmox remain experimental until their target-host firewall, guest image/template, storage, teardown and privilege assumptions have been validated on real infrastructure.

## Diagnostics

GitHub recommends preserving ephemeral-runner diagnostics externally. RunnerHub retains a **bounded startup-only diagnostic tail** when a provider container fails before a job starts. For Docker workers this is limited to the last 80 container-log lines, capped to 4096 bytes, passed through credential/token redaction, and stored with the worker record before automatic cleanup. This is intended to preserve registration/bootstrap failures that would otherwise disappear with the ephemeral container.

RunnerHub deliberately does **not** retain provider stdout/stderr after a job has started and does not ingest workflow job logs, because those streams can contain repository secrets and sensitive build output. The startup diagnostic tail is supplementary evidence, not a general-purpose log archive; operators should still use GitHub's normal job logs and an explicitly designed external diagnostics sink for broader retention.
