# Firecracker provider

The Firecracker provider is experimental and intended for stronger per-job isolation on Linux/KVM hosts.

Requirements:

- `/dev/kvm` available to the RunnerHub agent;
- a Firecracker binary, kernel image, and runner root filesystem;
- `mkfs.ext4`;
- `iproute2`;
- a host bridge configured with outbound connectivity and DHCP for guests;
- `RUNNERHUB_FIRECRACKER_BRIDGE` set to that bridge.

RunnerHub creates a deterministic TAP interface per worker, attaches it to the configured bridge, supplies a read-only ext4 seed disk containing the JIT configuration, copies the base root filesystem for each job, and deletes the TAP/rootfs after teardown.

The guest image must mount/read the RunnerHub seed disk, configure `eth0` with DHCP, and start the runner. `deploy/firecracker/runnerhub-init.sh` is the starting point for guest initialization.

Do not treat this provider as a production untrusted-code boundary until jailer, seccomp, guest image provenance, bridge/firewall policy, and teardown have been validated on the target host.
