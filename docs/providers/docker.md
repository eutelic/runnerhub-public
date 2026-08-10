# Docker provider

The Docker provider is the default execution backend for trusted private repositories.

It uses exact container IDs, an image allowlist, resource limits, a tmpfs work directory, `no-new-privileges`, no published ports, and no Docker socket inside job containers. JIT configuration is written to a `0600` host file and mounted read-only into the worker, where `run.sh --jitconfig` consumes it.

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

The agent container mounts `/var/run/docker.sock` because the agent is trusted infrastructure that creates and deletes ephemeral worker containers. **The worker/job containers do not receive the Docker socket.** The agent also bind-mounts `/var/lib/runnerhub` at the same absolute path inside and outside the container; this is required because the host Docker daemon must be able to resolve the JIT configuration bind path created by the agent.

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
