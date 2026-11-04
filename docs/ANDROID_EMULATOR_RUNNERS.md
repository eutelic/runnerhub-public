# Dedicated KVM-backed Android emulator workers

This guide covers Example Mobile App's **real native Android emulator / touch** workflow. A passing prerequisite probe is **not** proof of a booted emulator. The required acceptance is a successful `Native Android Emulator Smoke` workflow with the scene-ready and lower-screen touch-start / drag / release evidence in uploaded artifacts.

## Why the TrueNAS Docker worker cannot be reused

A capability probe on the existing `runnerhub` container reported `kvm_device_exists=false` and `kvm_read_write_access=false`. The standard `container-trusted` worker does not expose host devices, and its agent itself is Docker-contained on TrueNAS. `container-dind` is **not** a safe workaround: it enables an isolated daemon but does not grant access to `/dev/kvm`. Do not mount the host Docker socket or grant `--privileged` to normal CI jobs to solve this.

Use a **dedicated native Linux x86_64 machine or VM** that has KVM exposed to the unprivileged runner account. Nested virtualization requires an explicit hypervisor setting if the worker is itself a VM. Keep this machine isolated from production NAS data and credentials. Only connect private, trusted repositories; native `host-persistent` workers execute directly under the runner account and are not container-isolated.

## Recommended: provision from the RunnerHub console

The dashboard now supports **Hosts → Add machine → Runner type: Native Android emulator / KVM agent**. This installs the runner for you over pinned SSH; the operator does not need to copy installation commands to the host.

1. Keep the existing `docker-host` **Docker agent online**. Enter the host's SSH/Tailscale address (for example `TRUENAS_HOST`), existing SSH user (`docker-host`), and a **distinct RunnerHub name** (`android-kvm-host`). Select **Native Android emulator / KVM agent**, not Standard Docker CI. The wizard adds `android-emulator` to the host placement tags.
2. Click **Scan host key**, compare it with the host's independently verified `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256`, and supply the required SSH/sudo authentication. Docker must be installed on the machine; the installer uses it only to obtain a trusted RunnerHub native agent binary, **not** to run Android jobs or give the native account Docker access. The installer does not remove, restart, or reconfigure the existing `runnerhub-agent` Docker container. It keeps the reusable GitHub runner distribution root-owned under `/usr/local/share/runnerhub-android/actions-runner`; only the per-job copy under the native agent's state is writable by the unprivileged runner.
3. Click **Set up Android KVM agent**. The server validates Linux/amd64, KVM ioctl 12 under the dedicated `runnerhub-android` system account, 4 CPU cores, 8 GiB available RAM, and 20 GiB free disk. It downloads official GitHub Actions runner **2.338.2**, verifies its published SHA-256, extracts the agent binary from the configured RunnerHub image, and enables an independent `runnerhub-android-agent` systemd service. A short-lived enrollment token is deleted after the control plane confirms a heartbeat.
4. Confirm that RunnerHub shows both `docker-host` (Docker) and `android-kvm-host` (native process) online. On the native record, `hardware.kvm=true` must be a **system-verified capability**. If setup fails, consult the inline error and the native agent's own `journalctl -u runnerhub-android-agent` logs; the standard Docker host should remain intact.
5. In `example-org/mobile-app`'s custom profiles, click **Fill Android emulator profile** and save `android-emulator`. Confirm that its listener is ready, then trigger the real emulator smoke test twice.

**Release prerequisite:** The dashboard, KVM scheduler, and native agent changes must pass validation, merge, and be deployed to the control plane and published agent image before this new console option can work. Do not use an old `:latest` native agent binary with a new control plane.

### Manual installation (advanced fallback)

1. Enable Intel VT-x / AMD-V in firmware and allow `/dev/kvm` inside the VM, if applicable. Install `qemu-kvm`, `python3`, `unzip`, `curl` and Java 17 on the host. Grant only the dedicated runner account membership in the `kvm` group (log out/in to activate); do not run `chmod 666 /dev/kvm`.
2. Run `bash scripts/android-emulator-host-preflight.sh /path/to/runner-data` **as that runner account**. It fails on wrong architecture, inaccessible KVM ioctl, fewer than 4 CPUs, under 8 GiB **available** RAM (not merely installed RAM), or under 20 GiB available disk. Use an exclusive, non-production account/disk. This initial test does not boot Android.
3. Download and verify a pinned GitHub Actions runner tarball from the official `actions/runner` release. Extract to a directory owned by that account, such as `$HOME/actions-runner`. RunnerHub's native process provider launches that directory's `run.sh` with ephemeral JIT configuration; do **not** run a separately registered long-lived GitHub runner from the same directory.
4. Build or install the matching RunnerHub agent binary on the Linux host and enroll it separately with:
   
   ```bash
   export RUNNERHUB_PROVIDER=process
   export RUNNERHUB_HOST_NAME=kinetic-android-host
   # Set the same operator-chosen value on another agent only when both share hardware.
   export RUNNERHUB_PLACEMENT_DOMAIN_ID=compute-machine-01
   export RUNNERHUB_RUNNER_DIR="$HOME/actions-runner"
   export RUNNERHUB_STATE_DIR="$HOME/.local/state/runnerhub"
   export RUNNERHUB_CONTROL_PLANE_URL="https://YOUR-PROTECTED-RUNNERHUB-URL"
   export RUNNERHUB_ENROLLMENT_TOKEN="ISSUED-SHORT-LIVED-ENROLLMENT-TOKEN"
   runnerhub-agent
   ```

   Use TLS or a separately protected Tailscale connection and keep credentials outside your checkout. Agent heartbeat automatically advertises the **system** tag `hardware.kvm=true` only when the Linux AMD64 agent's own OS user can open `/dev/kvm` and successfully call `KVM_GET_API_VERSION` (12). If access later disappears, the next heartbeat removes the system tag.
5. In RunnerHub's **Hosts** panel, apply the **operator tag** `android-emulator` to this dedicated host. Under Example Mobile App's custom profiles, use **Fill Android emulator profile** to populate the constrained native configuration; generic profiles cannot select `host-persistent` execution. Create Example Mobile App's custom profile `android-emulator`, using:
   
   | Setting | Required |
   |---|---|
   | GitHub `runs-on` | `runnerhub-android-emulator` |
   | Isolation | `host-persistent` (native) |
   | Required machine tags | `android-emulator`, `hardware.kvm` |
   | CPU | at least 4 cores |
   | RAM | at least 8 GiB |
   | Disk reservation | at least 20 GiB |
   | Max concurrency | 1 |
   | Auto-size | disabled |
   | Repository | `example-org/mobile-app`, private |

   The API rejects an incorrectly configured emulator profile, and the scheduler independently requires the native process provider, Linux/amd64, verified **SystemTags** (not user-written tags), healthy state, and available resources. A user tag `hardware.kvm` cannot trick the scheduler.

## Toolchain and acceptance

The Example Mobile App workflow installs Java 17, Android SDK API 36/build-tools 36.0.0, Google APIs Android 15 x86_64 emulator image, and the pinned Godot 4.7.2 editor/debug export template. On the dedicated *persistent native host*, the Android SDK/toolcache are reusable across jobs; optionally preinstall them under the runner account using `sdkmanager` and prime Godot archives in a user-owned cache. Use **only disposable debug emulator keystores**; never copy the production Android signing key onto this host. Leave enough headroom beyond the reserved 8 GiB for the OS.

Before switching Example Mobile App's emulator workflow to `runnerhub-android-emulator`, confirm the host advertises the verified system capability and the custom scale-set listener is ready. Trigger `Native Android Emulator Smoke` at least twice, then verify the real gameplay scene launches, lower-screen touch begins, drags and releases, and `godot-native-android-emulator-smoke` artifacts contain the probe and diagnostics. Keep any GitHub-hosted fallback explicit while provisioning if the repository policy permits it; never silently treat an unexecuted test as passing.

If no compatible host is connected, RunnerHub reports **no compatible online hosts** with a KVM-specific capacity explanation. It must not run this job on a normal TrueNAS Docker worker. Real host provisioning and successful Android-emulator runs are required before considering KVM support accepted.
