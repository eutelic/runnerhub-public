# Deploy RunnerHub on TrueNAS SCALE

This is the supported image-based deployment path for TrueNAS SCALE 24.10+.

RunnerHub now uses a **deploy first, connect repositories from the browser later** flow:

1. create one GitHub App;
2. deploy RunnerHub with the App's numeric App ID, client ID, client secret and private key;
3. open the RunnerHub dashboard;
4. sign in with GitHub;
5. install/manage the RunnerHub GitHub App and choose repositories;
6. click **Connect** beside a private repository;
7. use `runs-on: runnerhub` in that repository.

You no longer put a GitHub installation ID or repository URL in the TrueNAS YAML.

## Security model

The RunnerHub Docker agent mounts `/var/run/docker.sock` because it creates and deletes ephemeral runner containers. Treat the agent as root-equivalent trusted infrastructure. Workflow containers do not receive the host Docker socket.

The default `container-trusted` mode is intended for trusted private repositories. Use `container-dind` only when trusted workflows require Docker container actions, services or job containers.

Publisher runners use `container-dind` as a separate per-repository runner class. To make that class available while keeping ordinary runners on `container-trusted`, enable privileged-worker capability at deployment time and leave the default isolation trusted. Repositories still opt in individually from the dashboard.


Do not expose the dashboard directly to the public Internet. A LAN or Tailscale URL is appropriate. The GitHub OAuth callback and App setup flow are browser redirects, so the RunnerHub URL only needs to be reachable by the browser completing setup.

## 1. Prepare storage

Create:

```text
/mnt/POOL/runnerhub
/mnt/POOL/runnerhub/data
/mnt/POOL/runnerhub/agent
/mnt/POOL/runnerhub/secrets
```

Replace `tank` with your TrueNAS pool.

## 2. Create the GitHub App

Create one GitHub App, preferably owned by the same organization as RunnerHub.

Repository permissions:

```text
Administration: Read & write
Actions: Read-only
Metadata: Read-only
```

Webhook:

```text
Inactive
```

Set the App URLs to your RunnerHub dashboard URL. If RunnerHub will be opened at:

```text
http://TRUENAS_HOST:8787
```

configure:

```text
Homepage URL:
http://TRUENAS_HOST:8787

Callback URL:
http://TRUENAS_HOST:8787/auth/github/callback

Setup URL:
http://TRUENAS_HOST:8787/auth/github/setup
```

Enable redirect after installation/update for the Setup URL.

RunnerHub performs the GitHub sign-in explicitly, so it does not require "Request user authorization during installation" to be enabled.

Record:

```text
App ID          numeric ID from the GitHub App settings
Client ID       Iv1...
Client secret   generated in the GitHub App settings
App slug        the URL slug from github.com/apps/<slug>
Private key     generated .pem
```

Do **not** choose repositories yet if you do not want to. Repository selection happens after RunnerHub is deployed.

## 3. Store the GitHub App secrets

### Local RunnerHub secrets

Place:

```text
/mnt/POOL/runnerhub/secrets/github-app.pem
/mnt/POOL/runnerhub/secrets/github-client-secret
```

Then:

```bash
sudo chmod 600 /mnt/POOL/runnerhub/secrets/github-app.pem
sudo chmod 600 /mnt/POOL/runnerhub/secrets/github-client-secret
```

### Scale Secrets

Both files can instead be supplied from protected Scale Secrets paths. Pass those paths to the generator with:

```text
--github-app-key-path
--github-client-secret-path
```

Mode `0444` is accepted for managed secret files when their containing directory is mode `0700`.

## 4. Host preflight

Default paths:

```bash
sudo bash deploy/truenas/preflight-host.sh /mnt/POOL/runnerhub
```

External secret paths:

```bash
sudo bash deploy/truenas/preflight-host.sh \
  /mnt/POOL/runnerhub \
  /mnt/POOL/app-secrets/secrets/runnerhub_github_app \
  /mnt/POOL/app-secrets/secrets/runnerhub_github_client_secret
```

The preflight checks TrueNAS/Docker readiness, RunnerHub datasets, the App private key, and the OAuth client secret.

## 5. Verify the release images

RunnerHub publishes:

```text
ghcr.io/eutelic/runnerhub-control
ghcr.io/eutelic/runnerhub-agent
ghcr.io/eutelic/runnerhub-bootstrap
ghcr.io/eutelic/runnerhub-scaleset
ghcr.io/eutelic/runnerhub-deployer
```

Verify the release:

```bash
bash deploy/truenas/check-images.sh --tag latest
```

Or pin one commit:

```bash
bash deploy/truenas/check-images.sh --tag sha-FULL_COMMIT_SHA
```

If the packages are private, TrueNAS needs a GHCR credential that can pull packages from `eutelic`.

## 6. Generate the TrueNAS YAML

Example using a Tailscale/LAN URL:

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-app-id 123456789 \
  --github-client-id Iv1.EXAMPLE \
  --github-app-slug runnerhub-self-hosted-ci \
  --public-url http://TRUENAS_HOST:8787 \
  --output runnerhub-truenas.yaml
```

With external secret files:

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-app-id 123456789 \
  --github-client-id Iv1.EXAMPLE \
  --github-app-slug runnerhub-self-hosted-ci \
  --public-url http://TRUENAS_HOST:8787 \
  --github-app-key-path /mnt/POOL/app-secrets/secrets/runnerhub_github_app \
  --github-client-secret-path /mnt/POOL/app-secrets/secrets/runnerhub_github_client_secret \
  --output runnerhub-truenas.yaml
```

Useful options:

```text
--port 8787
--isolation container-trusted
--allow-privileged-workers true   # permits opt-in runnerhub-publisher pools
--runnerhub-tag sha-FULL_COMMIT_SHA
--runner-image ghcr.io/actions/actions-runner:2.337.0@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4
--dind-image docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0
```

There is intentionally no `--github-installation-id` and no `--github-url`.

## 7. Install RunnerHub

In TrueNAS:

1. **Apps > Discover Apps**
2. **Custom App > Install via YAML**
3. Name it `runnerhub`
4. Paste `runnerhub-truenas.yaml`
5. Save

Startup order is now:

```text
init-secrets
    ↓
control
    ↓
agent
    ↓
dynamic scale-set manager
```

The scale-set manager is healthy with zero repositories connected. Once repositories are connected, it reports healthy only when every configured repository listener has reached GitHub scale-set/message-session readiness. A listener failure clears readiness until reconciliation succeeds again.

No bootstrap container needs to create a repository pool before the UI can start.

## 8. Verify the pre-onboarding deployment

Run:

```bash
sudo bash deploy/truenas/verify.sh \
  /mnt/POOL/runnerhub \
  http://127.0.0.1:8787
```

A valid fresh deployment may report:

```text
[note] no repositories connected yet; finish setup from the RunnerHub dashboard
```

That is expected.

The verifier checks:

- persistent RunnerHub credentials;
- App key and OAuth client secret;
- control health;
- GitHub browser authentication being enabled;
- API authorization boundaries;
- online Docker host;
- persisted agent identity;
- dynamic scale-set manager health when host Docker access is available;
- stale workers and orphan DinD sidecars.

## 9. Sign in and connect repositories

Open the exact URL configured as `--public-url`.

The dashboard shows:

```text
Sign in with GitHub
```

After sign-in, choose:

```text
Install / manage repositories
```

GitHub then lets you install the RunnerHub App on your user/org and choose **All repositories** or **Only select repositories**.

After GitHub redirects back to RunnerHub, the dashboard lists repositories available through that App installation.


### Enable a publisher runner for a repository

For Docker builds and image publishing, deploy RunnerHub with publisher capability enabled while leaving the normal runner class trusted:

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-app-id 123456789 \
  --github-client-id Iv1.EXAMPLE \
  --github-app-slug runnerhub-self-hosted-ci \
  --public-url http://TRUENAS_HOST:8787 \
  --isolation container-trusted \
  --allow-privileged-workers true \
  --output runnerhub-truenas.yaml
```

This does **not** make normal `runnerhub` jobs privileged. It only lets the host advertise the separate Docker-capable class. In the repository card, enable **Publisher runner** and use `runs-on: runnerhub-publisher` for Docker/image jobs. Normal CI continues to use `runs-on: runnerhub`.

Publisher pools are repository-scoped and can be disabled independently. They use a private Docker-in-Docker daemon rather than exposing the TrueNAS host Docker socket to workflow jobs.

For each private repository you want RunnerHub to serve, click:

```text
Connect
```

RunnerHub then:

1. verifies the selected repository belongs to an App installation accessible to the signed-in user;
2. creates a repository-scoped RunnerHub pool;
3. stores the GitHub installation ID with that pool;
4. exposes it to the dynamic scale-set manager;
5. starts an outbound GitHub scale-set listener;
6. creates/resolves the `runnerhub` scale set;
7. reports **Ready** only after the GitHub scale-set listener and message session are established.

The dashboard shows **Pending/Starting/Ready/Error** per repository. Creating the local pool is not treated as a successful GitHub connection. Listener failures are surfaced in the dashboard and retried by the dynamic manager.

No Compose edit or container restart is required.

The first GitHub user to complete sign-in becomes the local RunnerHub owner. That owner identity is persisted in the RunnerHub data directory. Browser sessions themselves are intentionally in memory and require sign-in again after a control-plane restart.

API-key administration remains available as a fallback.

## 10. Run a smoke workflow

In a connected repository:

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

Dispatch the workflow. The dynamic listener should receive demand, RunnerHub should create an ephemeral worker, and the worker should be removed afterward.

## 11. Optional post-onboarding verification

If you want `verify.sh` to assert one specific pool after connecting a repo, pass the pool ID and runner label:

```bash
sudo bash deploy/truenas/verify.sh \
  /mnt/POOL/runnerhub \
  http://127.0.0.1:8787 \
  POOL_ID \
  runnerhub \
  container-trusted
```

The pool ID is visible through the RunnerHub API/dashboard. When a pool ID is supplied, the verifier also requires that pool's runtime scale-set listener status to be `ready`.

## Docker workflow compatibility

Default:

```text
container-trusted
```

This does not provide Docker inside workflow jobs.

For trusted repositories that require Docker actions/services/job containers, generate with:

```bash
--isolation container-dind
```

This enables a private privileged Docker-in-Docker sidecar per worker rather than mounting the TrueNAS host Docker socket into workflow containers.

## Persistence and backup

Back up:

```text
data/
agent/
secrets/
```

The `data/` directory now also contains the persisted GitHub owner claim. The GitHub App private key and client secret may be local under `secrets/` or externally managed.

## Updates and rollback

The template defaults to `:latest` with `pull_policy: always`.

For a deterministic deployment:

```bash
--runnerhub-tag sha-FULL_COMMIT_SHA
```

Validate the image set first:

```bash
bash deploy/truenas/check-images.sh --tag sha-FULL_COMMIT_SHA
```

All RunnerHub services should always move together to the same release SHA.

## Optional automatic Custom App redeployments

RunnerHub can run a small deployment coordinator as a **separate TrueNAS Custom App**. Keep it separate from the main `runnerhub` app: this allows the deployer to update RunnerHub itself without being terminated by the redeploy it is coordinating.

The deployer never gives a workflow the TrueNAS API key or the host Docker socket. A workflow receives only a target-specific RunnerHub deployment token. The server-side policy maps that target to one fixed TrueNAS app and optional health URL.

### TrueNAS prerequisite: TLS

TrueNAS 25.04 revokes a user-linked API key if it is submitted over insecure HTTP/WebSocket transport. Configure HTTPS for the TrueNAS management/API endpoint before enabling the deployer. The deployer intentionally accepts only a `wss://` TrueNAS URL.

Create a dedicated local service user for the deployer and grant only the application roles it needs:

```text
APPS_READ
APPS_WRITE
```

Create a user-linked API key for that service user and save it to:

```text
/mnt/POOL/runnerhub-deployer/secrets/truenas-api-key
```

### Create the deployment policy

Generate a random workflow token and store only its SHA-256 digest in the deployer policy:

```bash
token="$(openssl rand -hex 32)"
printf '%s\n' "$token"
printf '%s' "$token" | sha256sum
```

Keep the plaintext token in the repository's GitHub Actions secret (for example `RUNNERHUB_DEPLOY_TOKEN`). Put the digest in a policy copied from `deployments.example.json`:

```json
{
  "targets": [
    {
      "id": "sample-app",
      "repository": "example-org/sample-app",
      "app": "sample-app",
      "image": "ghcr.io/example-org/sample-app:latest",
      "token_sha256": "REPLACE_WITH_64_HEX_SHA256",
      "health_url": "http://TRUENAS_HOST:3001/health",
      "timeout_seconds": 120
    }
  ]
}
```

The workflow cannot override `repository`, `app`, `image`, or `health_url`; those values are trusted server-side configuration. The repository and image fields are also written to the deployment audit from policy rather than accepted from workflow input.

Store the policy at:

```text
/mnt/POOL/runnerhub-deployer/config/deployments.json
```

Create a persistent audit directory:

```text
/mnt/POOL/runnerhub-deployer/data
```

### Install the standalone deployer

Copy `deploy/truenas/deployer.compose.yaml`, replace:

```text
/mnt/POOL/runnerhub-deployer
TRUENAS_HOST
runnerhub_deployer
```

and install it as a second TrueNAS Custom App, for example named `runnerhub-deployer`.

The default published port is `8789`. Keep this endpoint on LAN/Tailscale/private infrastructure rather than exposing it directly to the public Internet.

For a TrueNAS certificate signed by a private CA, mount the CA certificate and set `RUNNERHUB_TRUENAS_CA_FILE`. `RUNNERHUB_TRUENAS_TLS_INSECURE_SKIP_VERIFY=true` is available only as an explicit private-network/testing escape hatch; encrypted transport is still required.

### Trigger from GitHub Actions

A self-hosted RunnerHub job that can reach the deployer can request only its preconfigured target:

```yaml
deploy:
  needs: publish
  runs-on: runnerhub
  steps:
    - name: Redeploy TrueNAS app
      env:
        DEPLOY_TOKEN: ${{ secrets.RUNNERHUB_DEPLOY_TOKEN }}
        DEPLOY_URL: http://TRUENAS_HOST:8789
      run: |
        curl --fail-with-body \
          -X POST \
          -H "Authorization: Bearer $DEPLOY_TOKEN" \
          -H "Content-Type: application/json" \
          "$DEPLOY_URL/api/v1/deployments/sample-app" \
          -d "{"repository":"$GITHUB_REPOSITORY","sha":"$GITHUB_SHA","run_id":"$GITHUB_RUN_ID"}"
```

For each deployment the coordinator:

1. authenticates the target-specific token;
2. acquires a per-app deployment lock;
3. calls TrueNAS `app.pull_images(app, {"redeploy": true})` and waits for the job result;
4. waits for the app state to become `RUNNING`;
5. runs the configured HTTP health check, when present;
6. appends a sanitized record to `/data/deployments.jsonl`.

This first implementation is intended for the current `:latest` workflow. Immutable SHA image selection, GitHub OIDC authorization, rollback, and dashboard deployment history remain follow-up hardening.

## Troubleshooting

Check in this order:

1. `init-secrets`: App PEM/internal secret problems.
2. `control`: database, OAuth config, client secret, callback/public URL.
3. `agent`: Docker socket/enrollment/state path.
4. `scaleset`: dynamic manager access to the control API/App key.
5. after connecting a repo: App installation permission or GitHub scale-set listener errors.

If sign-in redirects to the wrong host, `RUNNERHUB_PUBLIC_URL` does not match the URL you configured in the GitHub App.

If a repo does not appear, use **Install / manage repositories** and verify that repository is selected in the GitHub App installation.

If a repo appears but connecting it fails, verify it is private and that the App still has Administration read/write, Actions read-only, and Metadata read-only on it.

No inbound GitHub webhook is required.
