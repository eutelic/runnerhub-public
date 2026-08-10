# RunnerHub alpha quickstart

This guide gets one Linux Docker host executing ephemeral GitHub Actions jobs for a **trusted private repository**.

RunnerHub is still alpha software. Start with a disposable or dedicated CI host, not a machine that contains unrelated secrets or irreplaceable workloads.

## 1. Prerequisites

- Linux host with Docker installed and usable by the account running `runnerhub-agent`.
- Go 1.26 if running RunnerHub from source.
- A private GitHub repository or organization you administer.
- A GitHub App installed only on the repositories RunnerHub should manage.

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

## 3. Generate RunnerHub secrets

Generate separate values; do not reuse one secret for multiple roles.

```bash
export RUNNERHUB_ENROLLMENT_TOKEN="$(openssl rand -hex 32)"
export RUNNERHUB_AGENT_SECRET="$(openssl rand -hex 32)"
export RUNNERHUB_ADMIN_KEY="$(openssl rand -hex 32)"
```

The enrollment token is bootstrap-only on an agent. After first enrollment, the agent stores a rotating per-host credential and the bootstrap token can be removed from that host.

## 4. Start the control plane

From the repository root:

```bash
export RUNNERHUB_GITHUB_APP_ISSUER='YOUR_GITHUB_APP_CLIENT_ID'
export RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE='/absolute/path/to/github-app.pem'
export RUNNERHUB_API_KEYS="${RUNNERHUB_ADMIN_KEY}:admin:local-admin"
export RUNNERHUB_DB='./runnerhub.db'

go run ./cmd/runnerhubd
```

The default listener is `127.0.0.1:8080`. Keep it loopback-only for this single-host quickstart.

Check it:

```bash
curl --fail http://127.0.0.1:8080/healthz
```

## 5. Enroll the Docker host

In a second shell on the same machine:

```bash
export RUNNERHUB_CONTROL_PLANE_URL='http://127.0.0.1:8080'
export RUNNERHUB_ALLOW_INSECURE_HTTP=true
export RUNNERHUB_ENROLLMENT_TOKEN='<same bootstrap token used by the control plane>'
export RUNNERHUB_PROVIDER='docker'
export RUNNERHUB_STATE_DIR="$HOME/.local/share/runnerhub"

go run ./cmd/runnerhub-agent
```

`RUNNERHUB_ALLOW_INSECURE_HTTP=true` is acceptable here only because traffic stays on loopback. For a remote host, use HTTPS or another explicitly trusted private transport layer.

Confirm enrollment:

```bash
curl --fail \
  -H "Authorization: Bearer ${RUNNERHUB_ADMIN_KEY}" \
  http://127.0.0.1:8080/api/v1/hosts
```

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
      - uses: actions/checkout@v4
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
