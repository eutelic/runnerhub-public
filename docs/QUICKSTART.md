# RunnerHub alpha quickstart

This guide gets one Linux Docker host executing ephemeral GitHub Actions jobs for a **trusted private repository** using the single-host Docker Compose stack.

RunnerHub is still alpha software. Start with a disposable or dedicated CI host, not a machine that contains unrelated secrets or irreplaceable workloads.

## 1. Prerequisites

- Linux host with Docker Engine and the `docker compose` plugin.
- `openssl` for local secret generation.
- A private GitHub repository or organization you administer.
- A GitHub App installed only on the repositories RunnerHub should manage.

Go 1.26 is only required if you choose to run RunnerHub from source instead of Compose.

GitHub recommends self-hosted runners for private repositories because pull requests from forks of public repositories can execute untrusted code on runner machines.

## 2. Create the GitHub App

Create a GitHub App and generate an RSA private key.

Use the minimum permissions needed for the scope you plan to run:

- **Repository-scoped pools:** repository **Administration: read and write**. GitHub requires Administration write permission for repository JIT runner configuration and deletion.
- **Organization-scoped pools:** organization **Self-hosted runners: read and write**. RunnerHub also reads runner-group configuration to fail closed when GitHub access is broader than the local pool policy.

Install the app only on the repositories RunnerHub should be able to see and manage.

For `RUNNERHUB_GITHUB_APP_ISSUER`, use the GitHub App client ID. GitHub also accepts the app ID as the JWT `iss` value, but currently recommends the client ID.

Official references:

- https://docs.github.com/en/rest/actions/self-hosted-runners
- https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps
- https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app

## 3. Generate the local Docker configuration

From the repository root:

```bash
bash scripts/init-docker-env.sh
```

This creates `deploy/docker/.env` with mode `0600` and independent random values for:

- the bootstrap enrollment token;
- the agent credential-signing secret;
- the local admin API key.

The generated `.env` is excluded by `.gitignore`.

Edit only these two required GitHub App values:

```text
RUNNERHUB_GITHUB_APP_ISSUER=YOUR_GITHUB_APP_CLIENT_ID
RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE=/absolute/path/to/runnerhub-github-app.pem
```

The private key file remains on the host and is mounted read-only into the control-plane container.

## 4. Start RunnerHub

```bash
docker compose \
  --env-file deploy/docker/.env \
  -f deploy/docker/compose.yaml \
  up -d --build
```

The stack starts:

- `control`: RunnerHub's control plane on host loopback only (`127.0.0.1:8080`);
- `agent`: the trusted Docker host agent, also using host networking so it can reach the loopback-only control plane.

The agent container has access to `/var/run/docker.sock` because it is trusted infrastructure responsible for creating and deleting workers. **The ephemeral job containers do not receive the Docker socket.**

Check startup:

```bash
docker compose \
  --env-file deploy/docker/.env \
  -f deploy/docker/compose.yaml \
  ps
```

If either service is unhealthy or restarting:

```bash
docker compose \
  --env-file deploy/docker/.env \
  -f deploy/docker/compose.yaml \
  logs -f control agent
```

## 5. Confirm the host enrolled

Load the generated configuration into your current shell and extract the admin key:

```bash
set -a
. deploy/docker/.env
set +a
export RUNNERHUB_ADMIN_KEY="${RUNNERHUB_API_KEYS%%:*}"
```

Check the control plane:

```bash
curl --fail http://127.0.0.1:8080/healthz
```

Check enrollment:

```bash
curl --fail \
  -H "Authorization: Bearer ${RUNNERHUB_ADMIN_KEY}" \
  http://127.0.0.1:8080/api/v1/hosts
```

You should see one `docker` provider host in the `online` state. The agent persists a rotating per-host credential under `/var/lib/runnerhub` so restarts do not require creating a new host identity.

## 6. Create an organization runner group

For the cleanest alpha setup, use an organization-scoped pool and a dedicated GitHub runner group.

In GitHub, create a runner group that:

- allows **private repositories only**;
- grants access only to the repository or repositories intended for this pool;
- optionally restricts workflows if you want a workflow-level boundary.

Record the runner-group numeric ID. RunnerHub validates the live GitHub runner-group posture before provisioning an organization runner.

## 7. Create a RunnerHub pool

Replace `YOUR_ORG`, `YOUR_REPO`, and `RUNNER_GROUP_ID`:

```bash
curl --fail-with-body \
  -X POST \
  -H "Authorization: Bearer ${RUNNERHUB_ADMIN_KEY}" \
  -H 'Content-Type: application/json' \
  http://127.0.0.1:8080/api/v1/pools \
  -d '{
    "name": "linux-docker",
    "scope": {
      "kind": "organization",
      "owner": "YOUR_ORG"
    },
    "labels": ["self-hosted", "runnerhub", "linux", "x64"],
    "isolation": "container-trusted",
    "resources": {
      "cpuMillis": 4000,
      "memoryBytes": 8589934592,
      "diskBytes": 21474836480
    },
    "minIdle": 0,
    "maxConcurrency": 2,
    "runnerGroupId": RUNNER_GROUP_ID,
    "image": "ghcr.io/actions/actions-runner:latest",
    "allowedRepositories": ["YOUR_ORG/YOUR_REPO"]
  }'
```

The response contains the generated pool ID. Save it:

```bash
export RUNNERHUB_POOL_ID='pool-...'
```

## 8. Point a workflow at RunnerHub

Use labels that match the pool:

```yaml
jobs:
  runnerhub-smoke:
    runs-on: [self-hosted, runnerhub]
    steps:
      - uses: actions/checkout@v6
      - run: |
          uname -a
          echo "RunnerHub smoke test passed"
```

Commit or dispatch the workflow so the job is waiting for a matching runner.

## 9. Provision the first direct-JIT worker

The current direct-JIT alpha API creates workers explicitly. Automatic demand-driven provisioning is handled by the separate scale-set adapter; direct-JIT webhook/queue automation is not yet the default path.

With the GitHub job queued, request one worker:

```bash
curl --fail-with-body \
  -X POST \
  -H "Authorization: Bearer ${RUNNERHUB_ADMIN_KEY}" \
  -H 'Content-Type: application/json' \
  "http://127.0.0.1:8080/api/v1/pools/${RUNNERHUB_POOL_ID}/workers" \
  -d '{
    "repository": "YOUR_ORG/YOUR_REPO",
    "public": false
  }'
```

RunnerHub should generate a short-lived JIT configuration, select the enrolled Docker host, start an ephemeral Actions runner container, execute one matching job, and then reconcile both the GitHub runner registration and provider container back to `deleted`.

Watch state:

```bash
watch -n 1 "curl -s -H 'Authorization: Bearer ${RUNNERHUB_ADMIN_KEY}' http://127.0.0.1:8080/api/v1/workers"
```

## 10. What success looks like

Before treating an installation as usable, verify all of these:

1. The host appears `online` in `/api/v1/hosts`.
2. Pool creation succeeds without a GitHub runner-group posture error.
3. A worker progresses through provisioning/registration to ready or busy.
4. The GitHub Actions job completes on the RunnerHub runner.
5. The worker eventually reaches `deleted`.
6. `docker ps -a --filter label=io.runnerhub.worker` shows no stale RunnerHub worker containers.
7. GitHub Actions runner settings show no stale ephemeral registration.

If one of those fails, preserve the RunnerHub audit output and the agent/control-plane logs before retrying. Those are the acceptance signals the repository's simulated lifecycle tests cannot replace.

## Stop RunnerHub

Keep control-plane data:

```bash
docker compose \
  --env-file deploy/docker/.env \
  -f deploy/docker/compose.yaml \
  down
```

To intentionally remove the Compose-managed control-plane volume as well, add `-v`. The agent credential/state bind mount under `/var/lib/runnerhub` is separate and is not deleted by `docker compose down -v`.

For source-based or multi-host installs, see the main README and provider documentation.
