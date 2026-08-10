# Security model

RunnerHub treats workflow code as arbitrary code with the privileges of its worker environment.

## Supported trust tiers

- `container-trusted`: disposable Docker/TrueNAS worker for trusted private repositories. No published ports and no host Docker socket by default.
- `host-persistent`: compatibility mode for trusted special hardware, macOS, or Windows using the process provider.
- `microvm`: Firecracker/KVM worker. The provider creates a fresh writable rootfs, per-worker TAP interface, and separate read-only seed disk containing only the short-lived JIT configuration.
- `vm-provider`: provider-created VM, currently Proxmox, bootstrapped with a generated cloud-init snippet containing only the JIT configuration.

Public/untrusted workflows are denied by default and should remain disabled until the microVM/VM path has received a dedicated live security review.

## GitHub assignment boundary

A RunnerHub pool is not considered safe merely because its local configuration contains an allowlist. GitHub runner groups are the assignment boundary for organization-scoped runners. RunnerHub inspects the actual GitHub runner-group visibility, public-repository setting, selected repositories, and workflow restrictions before creating or using a pool.

Repository-scoped pools may be used for trusted private repositories. RunnerHub does not claim that a repository-scoped local workflow allowlist is enforceable at GitHub's runner assignment boundary; use an organization pool with a workflow-restricted runner group when workflow-specific access control is required.

## Credentials

Long-lived GitHub App credentials remain in trusted control components; they are never delivered to job workers. In the default automatic Docker deployment, both `runnerhubd` and the scale-set listener need GitHub App authentication. The App PEM is mounted as a read-only Compose secret, copied to an owner-only runtime path, and both processes drop root privileges before normal operation. Workers receive only short-lived JIT configuration.

The scale-set listener uses a separate RunnerHub `scaler` API credential rather than the local admin key. That role is restricted to reading pool/worker state and posting scale-worker requests and scale lifecycle events; it cannot read hosts/audit data, drain hosts, modify pools, access cache mutation endpoints, or request direct-JIT workers.

The global enrollment token is a bootstrap credential. After first enrollment, the agent persists only its rotating per-host token in an owner-only credential file and can restart without the enrollment token. Per-host tokens expire and are refreshed during normal heartbeats.

Durable create commands must briefly retain JIT material while an agent is offline. RunnerHub encrypts those command payloads with AEAD using a key derived from the control-plane agent secret, decrypts them only for delivery to the authenticated host, scrubs the stored payload after acknowledgment, and prunes old acknowledged rows.

API keys are hashed in memory before comparison. Trusted identity headers are accepted only from a loopback reverse proxy. A control plane bound beyond loopback requires native TLS unless the operator deliberately enables the insecure-HTTP override for a separately protected network.

## Privilege

The trusted Docker host agent mounts `/var/run/docker.sock` because it creates and removes ephemeral workers. The worker containers themselves never receive that socket. Workers publish no ports, run with `no-new-privileges`, and receive explicit CPU/memory/workdir limits. Privileged capabilities require a future explicit profile/policy. Container isolation is not described as equivalent to a VM security boundary.

Ephemeral Docker runner images are refreshed before worker startup by default. If an operator changes the pull policy to `missing` or `never`, they become responsible for keeping the runner image current through an explicit image rollout process.

Firecracker and Proxmox remain experimental until their target-host firewall, guest image/template, storage, teardown and privilege assumptions have been validated on real infrastructure.

## Diagnostics

GitHub recommends preserving ephemeral-runner diagnostics externally. RunnerHub currently centralizes lifecycle/audit/health metadata but intentionally does **not** ingest workflow stdout or runner diagnostic files by default because those streams can contain job secrets or sensitive build output. A future diagnostics sink should be explicit, capped, separately retained and reviewed for secret handling.
