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
Pull requests: Read-only
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

After GitHub redirects back to RunnerHub, the dashboard lists repositories available through every installation of that App that the signed-in RunnerHub owner can access. Repositories are grouped by GitHub account, so a personal installation and one or more organization installations can be used by the same RunnerHub deployment.

### Personal repositories and multiple accounts

To use both organization repositories and personal repositories with one RunnerHub deployment, configure the GitHub App as **Any account** rather than **Only on this account**. GitHub only allows a private/owner-only App to be installed on the account that owns it.

Then use **Add / manage accounts** in RunnerHub and install the same GitHub App on each desired account:

1. install it on the organization, such as `eutelic`;
2. install it on the personal account, such as `example-user`;
3. choose **All repositories** or the specific repositories RunnerHub should access on each account.

RunnerHub discovers all accessible installations through the signed-in user's GitHub App user token and shows each account separately. No second RunnerHub deployment, PAT, or second GitHub App is required.

RunnerHub continues to reject public repositories by default. This is intentional because self-hosted runners attached to public repositories require a stronger untrusted-contribution threat model. Private personal repositories can be connected normally.


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

### Automatic CPU/RAM sizing

Each standard, publisher, or custom runner pool can opt into **Auto-size CPU/RAM from observed job usage**. Resource learning is pool-specific, so a repository's standard runner, publisher runner, and custom profiles learn independently.

The sizing policy is intentionally conservative:

- the most recent 50 valid completed jobs are considered;
- at least 5 valid jobs are required before an automatic upsize;
- CPU targets use the observed p95 per-container peak plus 25% headroom; RAM targets use the maximum recent valid per-container peak plus 25% headroom because memory is a hard failure boundary; targets are rounded to 0.25 CPU and 0.5 GiB RAM steps;
- automatic downsizing requires at least 25 valid jobs with at least two runtime samples each;
- ordinary failed jobs are excluded, while any recent OOM-killed job raises the memory floor even when it falls outside the p95 tail;
- impossible or incompatible telemetry is ignored rather than allowed to inflate the learned size;
- a recommendation is applied only when it still passes RunnerHub policy and can fit at least one compatible host.

For `container-dind` publisher/Docker-capable runners, the configured CPU and RAM values are **per outer container**. RunnerHub reserves one envelope for the Actions runner and one for its private DinD sidecar. Learning therefore sizes from the busiest individual outer container, not the sum of runner + sidecar usage. Aggregate usage is retained for diagnostics and display only.

Changing a pool's CPU, RAM, GPU request, isolation mode, runner image, or machine selector manually starts a new learning window. Turning auto-size on for an existing pool also starts fresh rather than immediately acting on usage collected while automation was disabled. The dashboard also provides **Reset learning** for one runner class and **Reset all learning** for every runner class in a repository. Resetting is non-destructive: historical worker/audit records remain available, but only runners created after the reset join the new learning cohort; an older runner that finishes after the reset is still excluded. Reset does not itself change the currently configured CPU/RAM.

Because runtime usage is sampled periodically, very short bursts can be missed. RunnerHub refuses to auto-downsize until at least 25 jobs have two or more runtime samples, and OOM kills are treated as memory-limit pressure. Users with unusually bursty workloads should still review the displayed CPU p95, RAM maximum, and learning coverage.

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

The first GitHub user to complete sign-in becomes the local RunnerHub owner. That owner identity is persisted in the RunnerHub data directory. Browser sessions are also persisted server-side in `data/github-sessions.json` with mode `0600`, so normal control-plane restarts and redeploys do not require another GitHub sign-in. Expiring GitHub App user access tokens are refreshed automatically while the 90-day browser session remains valid; signing out removes the persisted session.

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

The `data/` directory contains the persisted GitHub owner claim and `github-sessions.json`. The session file contains GitHub OAuth access/refresh credentials and is written with mode `0600`, so protect `data/` backups as sensitive material. The GitHub App private key and client secret may be local under `secrets/` or externally managed.

## Updates and rollback

The template defaults to `:latest` with `pull_policy: always`.

The control service passes the selected RunnerHub agent image to native SSH
onboarding as `RUNNERHUB_MACHINE_AGENT_IMAGE`. A KVM agent installed through
**Hosts → Set up KVM** therefore uses the same release tag as the TrueNAS
agent rather than an unrelated moving `:latest` tag. This setting is generated
by the template and should not be replaced with a different image revision.

**Existing Custom App upgrades:** merely pulling a newer control image will not add
new Compose environment variables. Regenerate the Custom App YAML with the
matching `prepare-compose.sh` release before updating, or add
`RUNNERHUB_MACHINE_AGENT_IMAGE` explicitly to the existing control service
with the *same immutable agent image reference* already used by the agent
service. Preserve the current state volumes and secrets; never reinstall
from scratch just to introduce this variable.

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

### Dashboard setup

Current RunnerHub releases can install or update the standalone TrueNAS redeployer from the **Deployments** card in the dashboard. The wizard asks for the TrueNAS WSS endpoint and a service account/API key with `APPS_READ` + `APPS_WRITE`. The redeployer port defaults to `8789`, but can be changed (for example to `8790` if another TrueNAS app already uses `8789`). RunnerHub discovers the host path backing the current app's `/data` mount through TrueNAS; the UI keeps a data-path field only as an override. The RunnerHub app name and health URL are prefilled.

RunnerHub then:

1. creates a random target-specific workflow token;
2. writes the TrueNAS API key and deployment policy beneath `/data/deployer-bootstrap` with private permissions;
3. generates the standalone deployer Compose configuration;
4. creates or updates the separate `runnerhub-deployer` TrueNAS Custom App through `app.create` / `app.update`;
5. records non-secret bootstrap state so the dashboard can show deployer health; and
6. returns the workflow token once so it can be saved as the repository Actions secret `RUNNERHUB_DEPLOY_TOKEN`.

The API key is never written to the RunnerHub database or embedded in the generated Compose YAML. Re-running the wizard rotates the deployment token and updates the standalone app.

The manual setup below remains available for installations that do not want to grant the RunnerHub control plane one-time TrueNAS app-management access.

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

### Manage other TrueNAS apps from the RunnerHub dashboard

After the standalone redeployer and the RunnerHub control plane have both been
updated to the release that includes managed apps, open **Deployments → TrueNAS
apps**. RunnerHub discovers installed apps through the existing standalone
redeployer; the TrueNAS API key stays on the server.

1. Enable the **Enable redeploy** checkbox next to an app.
2. Click **Redeploy now** and confirm. The deployer pulls the configured images,
   redeploys the TrueNAS app, waits for `RUNNING`, and audits the result.
3. Uncheck the box to revoke dashboard redeployment permission without changing
   or uninstalling the TrueNAS app.

Enabled apps are saved to the deployer's persistent `/data/managed-apps.json`.
The existing RunnerHub self-redeploy target and the standalone deployer are
protected from this manual flow; they retain their own deployment controls.
No additional GitHub Action secrets, policy hashes, or YAML editing are required
for manual dashboard redeploys.

**Automatic redeploys after verified image publishing are not enabled by this
checkbox.** That integration is separate, so selecting a checkbox alone cannot
cause an unexpected redeploy when a workflow completes.

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
