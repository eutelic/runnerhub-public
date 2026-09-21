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
- a separate `docker:29.1.3-dind@sha256:173f284a4299164772a90f52b373e73e087583c0963f1334c9995f190ef6f3f5` sidecar dedicated to that worker;
- a private shared Unix-socket directory exposed to the runner as `DOCKER_HOST=unix:///var/run/docker.sock`;
- shared work and runner-externals paths so the inner Docker daemon can launch GitHub container actions correctly;
- a shared **runner network namespace** (`--network container:<runner-id>`) so service-container ports published by the inner Docker daemon are reachable from normal runner steps at the expected host-style addresses.

The host's `/var/run/docker.sock` is still **not** mounted into the workflow runner or into nested workload containers.

The sidecar is labeled with the exact RunnerHub worker/pool identity and its parent runner container ID. RunnerHub can therefore recover the sidecar for inspection/cleanup even if local worker-state files are missing. Because the DinD sidecar joins the runner's network namespace, cleanup stops/removes the sidecar **before** the runner container that owns that namespace.

The DinD sidecar itself uses Docker `--privileged`. Docker treats privileged containers as highly elevated, so this is a materially weaker host-isolation boundary than `container-trusted`; it is not equivalent to a VM or microVM sandbox.

The infrastructure image is configurable:

```dotenv
RUNNERHUB_DIND_IMAGE=docker:29.1.3-dind@sha256:173f284a4299164772a90f52b373e73e087583c0963f1334c9995f190ef6f3f5
```

For reproducible installations, pin this to a tested Docker DinD version/digest rather than relying indefinitely on the moving tag.

### DinD resource-accounting limitation

The current scheduler models `pool.Resources` as one worker envelope. In `container-dind`, those CPU/memory limits apply to the runner container, while the privileged sidecar and containers created by the inner Docker daemon are not yet placed under one scheduler-accounted aggregate cgroup/resource envelope. DinD work and daemon storage are also bind-backed rather than using the trusted worker's bounded tmpfs work directory.

Pool concurrency and host capacity provide coarse bounds, but strict aggregate per-worker DinD CPU, memory, and disk accounting remains a hardening item. Do not use DinD for untrusted/adversarial workloads.

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
