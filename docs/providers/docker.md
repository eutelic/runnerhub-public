# Docker provider

The Docker provider is the MVP execution backend for trusted private repositories.

It uses exact container IDs, an image allowlist, resource limits, a tmpfs work directory, `no-new-privileges`, no published ports, and no Docker socket inside job containers. JIT configuration is written to a `0600` host file and mounted read-only into the worker, where `run.sh --jitconfig` consumes it.

## Single-host Docker Compose

The Compose stack under `deploy/docker/` starts both the RunnerHub control plane and a trusted Docker host agent.

From the repository root:

```bash
cp deploy/docker/.env.example deploy/docker/.env
```

Edit `deploy/docker/.env` and replace every placeholder. In particular:

- generate independent enrollment, agent, and API-key secrets;
- set the GitHub App client ID in `RUNNERHUB_GITHUB_APP_ISSUER`;
- set `RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE` to the **absolute host path** of the GitHub App PEM file.

Then start RunnerHub:

```bash
docker compose --env-file deploy/docker/.env -f deploy/docker/compose.yaml up -d --build
```

Check the services:

```bash
docker compose --env-file deploy/docker/.env -f deploy/docker/compose.yaml ps
docker compose --env-file deploy/docker/.env -f deploy/docker/compose.yaml logs -f control agent
```

The control plane listens only on host loopback (`127.0.0.1:8080`) in this single-machine profile. The agent uses host networking to reach it without exposing the control API to the LAN.

The agent container mounts `/var/run/docker.sock` because the agent is trusted infrastructure that creates and deletes ephemeral worker containers. **The worker/job containers do not receive the Docker socket.** The agent also bind-mounts `/var/lib/runnerhub` at the same absolute path inside and outside the container; this is required because the host Docker daemon must be able to resolve the JIT configuration bind path created by the agent.

To stop the stack without deleting the persistent control-plane volume:

```bash
docker compose --env-file deploy/docker/.env -f deploy/docker/compose.yaml down
```

See [`../QUICKSTART.md`](../QUICKSTART.md) for GitHub App permissions, runner-group setup, pool creation, and the first real workflow.
