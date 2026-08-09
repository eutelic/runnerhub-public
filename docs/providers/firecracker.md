# Firecracker provider

The Firecracker provider is an experimental strong-isolation backend.

Requirements: Linux, `/dev/kvm`, Firecracker, an approved kernel, and a RunnerHub-prepared rootfs. Every worker receives a copy of the rootfs plus a fresh 16 MiB read-only seed disk containing the short-lived JIT config. The guest image should install `deploy/firecracker/runnerhub-init.sh` as its boot-time runner service.

Production deployment still needs host-specific jailer, network policy, image signing, and teardown acceptance testing before public/untrusted workflows are enabled.
