# Docker provider

The Docker provider is the default execution backend for trusted private repositories. It exposes two explicit isolation modes with different compatibility and security tradeoffs.

## `container-trusted` — default

This is the default and preferred mode when workflows do not require Docker container actions, service containers, or `container:` jobs.

It uses exact container IDs, an image allowlist, resource limits, a tmpfs work directory, `no-new-privileges`, no published ports, and **no Docker socket inside the workflow runner**. JIT configuration is stored under a host-private `0700` worker directory. Before the worker starts, RunnerHub explicitly assigns the JIT file to the official runner identity (`1001:1001`) and makes it owner-read-only (`0400`) instead of relying on file-creation mode bits surviving host ACL inheritance. This is required on TrueNAS/ZFS datasets where inherited ACLs can narrow a requested mode such as `0444` to `0440`. The file is still bind-mounted read-only and `run.sh --jitconfig` consumes it inside the isolated worker container.

The pool's `diskBytes` value is the **maximum tmpfs workdir size** in this mode. It is not charged against host filesystem capacity by the scheduler because Docker backs tmpfs with memory/swap and the runner container's memory cgroup remains the hard memory envelope. Disk-backed isolation modes continue to reserve host disk normally.

Because the workflow runner has no Docker daemon, shell, JavaScript, and composite actions work but GitHub workflow features that require Docker do not.

## `container-dind` — explicit trusted-workload opt-in

GitHub self-hosted runners need Docker for Docker container actions, service containers, and containerized jobs. RunnerHub supports those features without mounting the TrueNAS/host Docker socket into workflow jobs by creating a **private Docker-in-Docker sidecar for each worker**.

Enable it only for trusted workflows:

```dotenv
RUNNERHUB_POOL_ISOLATION=container-dind
RUNNERHUB_ALLOW_PRIVILEGED_WORKERS=true
```

Both settings are required. The control plane rejects a DinD pool when privileged workers are disabled, and the Docker agent independently refuses and does not advertise `container-dind` unless the same privileged-worker flag is enabled on that host. This keeps a misconfigured control plane from silently turning a default Docker host into a privileged execution host.

The private DinD daemon is started with Google's documented `mirror.gcr.io` Docker Hub cache as a registry mirror. Cached public Docker Hub pulls avoid Docker Hub pull-rate quota, while uncached images can fall back to Docker Hub.

For every DinD worker, RunnerHub creates:

- the normal ephemeral Actions runner container;
- a separate `docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0` sidecar dedicated to that worker;
- a private shared Unix-socket directory exposed to the runner as `DOCKER_HOST=unix:///var/run/docker.sock`;
- shared work and runner-externals paths so the inner Docker daemon can launch GitHub container actions correctly;
- a shared **runner network namespace** (`--network container:<runner-id>`) so service-container ports published by the inner Docker daemon are reachable from normal runner steps at the expected host-style addresses.

The host's `/var/run/docker.sock` is still **not** mounted into the workflow runner or into nested workload containers.

The sidecar is labeled with the exact RunnerHub worker/pool identity and its parent runner container ID. RunnerHub can therefore recover the sidecar for inspection/cleanup even if local worker-state files are missing. Because the DinD sidecar joins the runner's network namespace, cleanup stops/removes the sidecar **before** the runner container that owns that namespace.

The DinD sidecar itself uses Docker `--privileged`. Docker treats privileged containers as highly elevated, so this is a materially weaker host-isolation boundary than `container-trusted`; it is not equivalent to a VM or microVM sandbox.

The infrastructure image is configurable:

```dotenv
RUNNERHUB_DIND_IMAGE=docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0
```

For reproducible installations, pin this to a tested Docker DinD version/digest rather than relying indefinitely on the moving tag.

### Aggregate DinD resource budgets

For `container-dind` workers, `RunnerPool.Resources` is the intended **total logical-worker CPU/RAM budget** when the Docker host passes RunnerHub's aggregate-cgroup capability probe.

On a verified cgroup-v2 host using Docker's `systemd` or `cgroupfs` driver, RunnerHub:

- creates a bounded parent cgroup or transient systemd slice for the logical worker;
- launches both the Actions runner and privileged DinD sidecar with the same Docker `--cgroup-parent`;
- verifies both outer containers use that parent;
- launches a disposable nested container through the private DinD daemon during capability probing and requires it to remain below the bounded hierarchy;
- samples aggregate CPU/RAM, memory peak, and cgroup OOM evidence from the logical-worker parent;
- reserves one configured CPU/RAM envelope in scheduler capacity.

For example, a publisher configured for 2.5 CPU and 8 GiB reserves 2.5 CPU / 8 GiB for one active verified worker and 5 CPU / 16 GiB for two active verified workers.

When the probe fails, the Docker agent publishes a sanitized `docker.aggregate-fallback-reason` system tag (for example, `host_cgroup_inaccessible`, `parent_limits_unverified`, or `nested_cgroup_not_contained`). The dashboard shows that reason beside `legacy x2`. These reason codes do not grant permission to change cgroup privileges, and they are reported only after agent capability refresh.

The capability is fail-closed. If RunnerHub cannot prove the supported cgroup-v2 hierarchy, cannot create/read the bounded parent, or cannot verify nested-container containment, the host does **not** advertise aggregate resource groups. DinD remains available there, but the scheduler keeps the legacy conservative two-envelope reservation. If an aggregate-marked running worker later loses access to its aggregate cgroup, RunnerHub fails that worker rather than silently falling back to unverified x1 accounting.

Historical/fallback DinD jobs used independent per-container envelopes. RunnerHub therefore excludes those samples from the new DinD resource-learning cohort instead of attempting to reinterpret them as total-worker measurements.

The Docker agent is trusted/root-equivalent infrastructure because it controls the host Docker socket. **Image-based deployments do not grant host-cgroup administration by default.** Aggregate enforcement requires an explicit, trusted agent configuration that makes the host cgroup-v2 hierarchy writable at `RUNNERHUB_CGROUP_ROOT`, shares the host cgroup namespace, and provides the required host-cgroup privileges. Without that setup, the probe must fail and the scheduler must keep the conservative two-envelope reservation. Do not enable a writable host-cgroup mount or `SYS_ADMIN` solely to make dashboard reservations look smaller; evaluate the added host privilege and verify the containment probe first. Workflow containers do not receive the host Docker socket or host cgroup mount.

DinD work and daemon storage remain bind-backed and are reserved against host disk capacity; hard aggregate per-worker disk quotas are not part of this CPU/RAM change. This remains a trusted-workload compatibility mode, not an untrusted-code sandbox.

## Publisher runners

RunnerHub can expose a second, repository-scoped runner class for Docker/image work without changing the normal `runnerhub` pool. In the dashboard, connect the repository normally, then enable **Publisher runner**. Publisher jobs use:

```yaml
runs-on: runnerhub-publisher
```

The publisher pool is always created with `container-dind`, a private Docker daemon, and a default concurrency of one. The normal `runnerhub` pool remains `container-trusted`.

Publisher support is intentionally gated twice. The control plane must allow privileged workers and the Docker host must advertise DinD support:

```yaml
x-runnerhub-config:
  allow-privileged-workers: "true"
```

This setting permits creation of publisher pools; it does not make ordinary `runnerhub` workers privileged. Disabling a publisher pool removes it from dynamic scale-set configuration, stops its listener, and leaves the standard repository runner unchanged.

Use publisher runners only for trusted private repositories. The DinD sidecar is privileged and remains a weaker isolation boundary than the standard pool.

## Live DinD acceptance fixture

`tests/fixtures/runner-dind-smoke.yml` is intended for the real GitHub/TrueNAS acceptance pass. It verifies:

- the runner sees only the private Docker Unix socket;
- the private Docker daemon is operational;
- a GitHub Redis service container is actually reachable from a normal runner step on `127.0.0.1:6379`;
- a `docker://` container action executes successfully.

That live fixture complements provider unit tests for host gating, socket non-exposure, sidecar failure, orphan recovery, shared networking, and cleanup order.

## Runner image refresh

Ephemeral Docker workers use `docker run --pull always` by default, but the default runner reference is an **immutable digest pin**. Pulling `always` therefore refreshes the local cache without changing the selected runner identity. RunnerHub release validation advances that digest deliberately only after a real JIT acceptance run succeeds.

Override the pull behavior only when image lifecycle is managed separately:

```dotenv
RUNNERHUB_DOCKER_PULL_POLICY=always   # default
# RUNNERHUB_DOCKER_PULL_POLICY=missing
# RUNNERHUB_DOCKER_PULL_POLICY=never
```

Production policy requires a digest-pinned runner image. Do not replace the default with a moving tag such as `:latest`; advance the tested digest through the release process documented in [../RELEASE.md](../RELEASE.md).

## Single-host Docker Compose

The Compose stack under `deploy/docker/` starts the RunnerHub control plane, a trusted Docker host agent, and—under the `autoscale` profile—the outbound GitHub scale-set listener.

From the repository root:

```bash
bash scripts/init-docker-env.sh
```

Fill in the GitHub App client ID, installation ID, absolute PEM path, and repository URL in `deploy/docker/.env`, then run:

```bash
bash scripts/start-docker.sh
```

The bootstrap starts control + agent, waits for the Docker host to become online, creates the repository-scoped pool, and then starts the scale-set listener. The control plane listens only on host loopback (`127.0.0.1:8080`) in this single-machine profile, so GitHub does not need inbound access to RunnerHub.

The agent container mounts `/var/run/docker.sock` because the agent is trusted infrastructure that creates and deletes ephemeral worker containers. **The workflow worker containers do not receive the host Docker socket.** The agent also bind-mounts `/var/lib/runnerhub` at the same absolute path inside and outside the container; this is required because the host Docker daemon must resolve JIT/DinD bind paths created by the agent.

Check the complete stack:

```bash
docker compose \
  --env-file deploy/docker/.env \
  -f deploy/docker/compose.yaml \
  --profile autoscale ps
```

To stop it without deleting the persistent control-plane volume:

```bash
docker compose \
  --env-file deploy/docker/.env \
  -f deploy/docker/compose.yaml \
  --profile autoscale down
```

See [`../QUICKSTART.md`](../QUICKSTART.md) for GitHub App permissions and the first automatic workflow.
