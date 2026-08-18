# RunnerHub alpha quickstart

This guide gets one Linux Docker host automatically executing ephemeral GitHub Actions jobs for a **trusted private repository**.

The default path uses GitHub's runner scale-set listener protocol. RunnerHub opens outbound connections to GitHub; you do **not** need to expose the RunnerHub control plane or configure an inbound webhook.

RunnerHub is still alpha software. Start with a disposable or dedicated CI host, not a machine that contains unrelated secrets or irreplaceable workloads.

## 1. Prerequisites

- Linux host with Docker Engine and Docker Compose v2.
- `curl` and `openssl` on the host.
- A private GitHub repository you administer.
- A GitHub App installed only on the repository RunnerHub should manage.

## 2. Create the GitHub App

Create a GitHub App and generate an RSA private key.

For the default repository-scoped setup, configure the minimum runner permissions used by GitHub's scale-set APIs:

- Repository **Administration: read and write**.
- Repository **Metadata: read-only**.

Install the app on the target repository only.

Record these values:

1. The GitHub App client ID.
2. The installation ID. It is the number at the end of the installation settings URL, for example `.../settings/installations/12345678`.
3. The absolute host path to the generated PEM private key.
4. The repository URL, for example `https://github.com/acme/widgets`.

No RunnerHub webhook URL is required for this setup.

Official references:

- https://docs.github.com/en/actions/how-tos/manage-runners/use-actions-runner-controller/authenticate-to-the-api
- https://docs.github.com/en/actions/how-tos/manage-runners/use-actions-runner-controller/deploy-runner-scale-sets
- https://docs.github.com/en/actions/how-tos/manage-runners/use-actions-runner-controller/use-arc-in-a-workflow

## 3. Generate the local configuration

From the repository root:

```bash
bash scripts/init-docker-env.sh
```

This creates `deploy/docker/.env` with independent random enrollment, agent, admin, and constrained scale-set API credentials and mode `0600` permissions.

Edit the four GitHub values:

```dotenv
RUNNERHUB_GITHUB_APP_ISSUER=Iv1.your-client-id
RUNNERHUB_GITHUB_APP_INSTALLATION_ID=12345678
RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE=/absolute/path/to/runnerhub.pem
RUNNERHUB_GITHUB_URL=https://github.com/YOUR_ORG/YOUR_REPO
```

The generated defaults create a repository-scoped pool named `pool-docker`, a scale set named `runnerhub`, zero idle runners, and a maximum concurrency of two.

## 4. Start RunnerHub

Run:

```bash
bash scripts/start-docker.sh
```

The bootstrap script:

1. validates the local Docker/GitHub configuration;
2. builds and starts the loopback-only control plane;
3. enrolls the trusted Docker host agent and waits until it is online;
4. creates or updates the repository-scoped RunnerHub pool;
5. starts the outbound GitHub scale-set listener;
6. waits until the listener has authenticated, resolved/created its scale set, validated the RunnerHub pool, recovered active workers, and opened its GitHub message session.

It reports success only after that listener-ready point. Invalid App credentials, installation IDs, repository URLs, or scale-set setup therefore fail during bootstrap rather than appearing as a successful install with a restarting listener.

The control API remains on `127.0.0.1:8080`. Job containers do not receive the Docker socket. The trusted host agent does receive the Docker socket because it is the component responsible for creating and deleting ephemeral worker containers. The scale-set listener receives a separate constrained RunnerHub API credential rather than the local admin key.

Check the stack:

```bash
docker compose \
  --env-file deploy/docker/.env \
  -f deploy/docker/compose.yaml \
  --profile autoscale ps
```

Follow listener logs:

```bash
docker compose \
  --env-file deploy/docker/.env \
  -f deploy/docker/compose.yaml \
  --profile autoscale logs -f scaleset
```

## 5. Point a workflow at RunnerHub

The default scale-set name is `runnerhub`. Use it directly as `runs-on`:

```yaml
name: RunnerHub smoke
on:
  workflow_dispatch:

jobs:
  smoke:
    runs-on: runnerhub
    steps:
      - uses: actions/checkout@v6
      - run: |
          uname -a
          echo "RunnerHub smoke test passed"
```

Commit the workflow and dispatch it from GitHub.

The scale-set listener should receive demand from GitHub, request a short-lived JIT runner configuration, ask RunnerHub to schedule an ephemeral worker on the Docker host, and remove the worker after the job finishes.

## 6. Verify lifecycle cleanup

Inspect RunnerHub state:

```bash
ADMIN_KEY="$(sed -n 's/^RUNNERHUB_ADMIN_KEY=//p' deploy/docker/.env)"

curl --fail \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  http://127.0.0.1:8080/api/v1/hosts

curl --fail \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  http://127.0.0.1:8080/api/v1/workers
```

A successful first acceptance run should show all of the following:

1. The Docker host remains `online`.
2. A queued GitHub job causes a RunnerHub worker to appear automatically.
3. The worker reaches `busy` while the job is running.
4. Repository, workflow, job, queue, start, and completion metadata are recorded from scale-set events.
5. The job completes successfully in GitHub Actions.
6. The RunnerHub worker eventually reaches `deleted`.
7. `docker ps -a --filter label=io.runnerhub.worker` shows no stale RunnerHub job containers.
8. GitHub runner settings show no stale ephemeral runner registration.

## 7. Runner image freshness

The Docker provider defaults to:

```dotenv
RUNNERHUB_DOCKER_PULL_POLICY=always
```

That refreshes the configured ephemeral runner image before each worker starts. If you change it to `missing` or `never`, use an explicit image-rollout process so the runner binary does not become stale.

## 8. Tuning

The default env file exposes the two most useful autoscaling controls:

```dotenv
RUNNERHUB_MIN_RUNNERS=0
RUNNERHUB_MAX_RUNNERS=2
```

`RUNNERHUB_MAX_RUNNERS` is also used as the RunnerHub pool concurrency limit, so the GitHub listener cannot advertise more capacity than RunnerHub will schedule.

For a different workflow target, change `RUNNERHUB_SCALESET_NAME` and rerun `scripts/start-docker.sh`; use the same value in `runs-on`.

## Stop RunnerHub

```bash
docker compose \
  --env-file deploy/docker/.env \
  -f deploy/docker/compose.yaml \
  --profile autoscale down
```

Add `-v` only when you intentionally want to remove the Compose-managed control-plane database volume. The agent state under `/var/lib/runnerhub` is separate.

## Advanced paths

The control plane still exposes the direct-JIT worker API for diagnostics and explicit provisioning. Organization pools, custom runner groups, TrueNAS, Proxmox, Firecracker, and persistent process hosts are separate advanced paths and should be validated independently before production use.

For TrueNAS specifically, `RUNNERHUB_TRUENAS_ROOT` is required and must point at a dedicated dataset path for RunnerHub worker state. Do not reuse an unrelated applications or database dataset by accident.
