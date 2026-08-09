# Security model

RunnerHub treats workflow code as arbitrary code with the privileges of its worker environment.

## Supported trust tiers

- `container-trusted`: disposable Docker/TrueNAS worker for trusted private repositories. No published ports and no host Docker socket by default.
- `host-persistent`: compatibility mode for trusted special hardware, macOS, or Windows using the process provider.
- `microvm`: Firecracker/KVM worker. The provider creates a fresh writable rootfs and a separate read-only seed disk containing only the short-lived JIT configuration.
- `vm-provider`: provider-created VM, currently Proxmox, bootstrapped with a generated cloud-init snippet containing only the JIT configuration.

Public/untrusted workflows are denied by default and should remain disabled until the microVM/VM path has received a dedicated security review.

## Credentials

Long-lived GitHub App credentials remain in the control plane. Workers receive only JIT configuration. Enrollment tokens bootstrap per-host HMAC identities; API keys are hashed in memory before comparison. Secret-bearing files are created mode `0600` and are deleted with the worker lifecycle.

## Privilege

The Docker provider does not publish ports or mount `/var/run/docker.sock`. Privileged capabilities require a future explicit profile/policy. Container isolation is not described as equivalent to a VM security boundary.
