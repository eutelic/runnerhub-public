# Docker provider

The Docker provider is the default execution backend for trusted private repositories. It exposes two explicit isolation modes with different compatibility and security tradeoffs.

## `container-trusted` — default

This is the default and preferred mode when workflows do not require Docker container actions, service containers, or `container:` jobs.

It uses exact container IDs, an image allowlist, resource limits, a tmpfs work directory, `no-new-privileges`, no published ports, and **no Docker socket inside the workflow runner**. JIT configuration is stored under a host-private `0700` worker directory; the JIT file itself is `0444` because the official runner image executes as a non-root user and must be able to read the read-only bind mount. `run.sh --jitconfig` consumes it inside the isolated worker container.

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

For every DinD worker, RunnerHub creates:

- the normal ephemeral Actions runner container;
- a separate `docker:dind` sidecar dedicated to that worker;
- a private shared Unix-socket directory exposed to the runner as `DOCKER_HOST=unix:///var/run/docker.sock`;
- shared work and runner-externals paths so the inner Docker daemon can launch GitHub container actions correctly;
- a shared **runner network namespace** (`--network container:<runner-id>`) so service-container ports published by the inner Docker daemon are reachable from normal runner steps at the expected host-style addresses.

The host's `/var/run/docker.sock` is still **not** mounted into the workflow runner or into nested workload containers.

The sidecar is labeled with the exact RunnerHub worker/pool identity and its parent runner container ID. RunnerHub can therefore recover the sidecar for inspection/cleanup even if local worker-state files are missing. Because the DinD sidecar joins the runner's network namespace, cleanup stops/removes the sidecar **before** the runner container that owns that namespace.

The DinD sidecar itself uses Docker `--privileged`. Docker treats privileged containers as highly elevated, so this is a materially weaker host-isolation boundary than `container-trusted`; it is not equivalent to a VM or microVM sandbox.

The infrastructure image is configurable:

```dotenv
RUNNERHUB_DIND_IMAGE=docker:dind
```

For reproducible installations, pin this to a tested Docker DinD version/digest rather than relying indefinitely on the moving tag.

### DinD resource-accounting limitation

The current scheduler models `pool.Resources` as one worker envelope. In `container-dind`, those CPU/memory limits apply to the runner container, while the privileged sidecar and containers created by the inner Docker daemon are not yet placed under one scheduler-accounted aggregate cgroup/resource envelope. DinD work and daemon storage are also bind-backed rather than using the trusted worker's bounded tmpfs work directory.

Pool concurrency and host capacity provide coarse bounds, but strict aggregate per-worker DinD CPU, memory, and disk accounting remains a hardening item. Do not use DinD for untrusted/adversarial workloads.

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
