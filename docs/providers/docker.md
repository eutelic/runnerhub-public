# Docker provider

The Docker provider is the default execution backend for trusted private repositories. It exposes two explicit isolation modes with different compatibility/security tradeoffs.

## `container-trusted` — default

This is the default and preferred mode when workflows do not require Docker container actions, service containers, or `container:` jobs.

It uses exact container IDs, an image allowlist, resource limits, a tmpfs work directory, `no-new-privileges`, no published ports, and **no Docker socket inside the workflow runner**. JIT configuration is written to a `0600` host file and mounted read-only into the worker, where `run.sh --jitconfig` consumes it.

Because the workflow runner has no Docker daemon, JavaScript/composite/shell actions work but GitHub workflow features that require Docker do not.

## `container-dind` — opt-in Docker compatibility

GitHub's self-hosted runner model requires Docker for Docker container actions, service containers, and containerized jobs. To support those without mounting the TrueNAS/host Docker socket into workflow jobs, RunnerHub can create a **per-worker Docker-in-Docker sidecar**.

Enable it only for trusted workflows:

```dotenv
RUNNERHUB_POOL_ISOLATION=container-dind
RUNNERHUB_ALLOW_PRIVILEGED_WORKERS=true
```

Both settings are required. The control plane rejects a `container-dind` pool when privileged workers are disabled.

For every DinD worker, RunnerHub creates:

- the normal ephemeral Actions runner container;
- a separate `docker:dind` sidecar dedicated to that worker;
- a private shared Unix-socket directory exposed to the runner as `DOCKER_HOST=unix:///var/run/docker.sock`;
- shared work and runner-externals paths so the inner Docker daemon can launch GitHub container actions correctly.

The host's `/var/run/docker.sock` is still **not** mounted into the workflow runner or into the inner workload containers.

However, the DinD sidecar itself uses Docker `--privileged`. That is a materially weaker host-isolation boundary than `container-trusted`; Docker explicitly treats privileged containers as highly elevated. RunnerHub therefore keeps this mode administrator-gated and intended only for trusted private-repository CI.

The infrastructure image is configurable:

```dotenv
RUNNERHUB_DIND_IMAGE=docker:dind
```

For production reproducibility, pin this to a tested Docker DinD image/digest rather than relying indefinitely on the moving tag.

DinD's bind-backed work/daemon state also does not currently enforce the same tmpfs byte limit as `container-trusted`; host capacity/pool concurrency remain the primary bounds. Treat disk-quota enforcement for DinD as a remaining hardening item.

## Runner image refresh

Ephemeral Docker workers use `docker run --pull always` by default. This is deliberate: the default GitHub runner image uses the moving `latest` tag while scale-set JIT runners disable in-process runner updates, so reusing a stale local image indefinitely can eventually make the runner ineligible for new jobs.

Override the behavior only when image lifecycle is managed separately:

```dotenv
RUNNERHUB_DOCKER_PULL_POLICY=always   # default
# RUNNERHUB_DOCKER_PULL_POLICY=missing
# RUNNERHUB_DOCKER_PULL_POLICY=never
```

For reproducible installations, use RunnerHub's image allowlist/digest-pin policy together with an explicit image rollout process rather than silently disabling refresh on a moving tag.

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

The agent container mounts `/var/run/docker.sock` because the agent is trusted infrastructure that creates and deletes ephemeral worker containers. **The worker/job containers do not receive the host Docker socket.** The agent also bind-mounts `/var/lib/runnerhub` at the same absolute path inside and outside the container; this is required because the host Docker daemon must be able to resolve JIT and DinD bind paths created by the agent.

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
