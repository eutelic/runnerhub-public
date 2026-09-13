# Deploy RunnerHub on TrueNAS SCALE

This is the supported **image-based** deployment path for TrueNAS SCALE 24.10 or newer. TrueNAS pulls prebuilt RunnerHub images from GHCR; the NAS does not need to build RunnerHub or install Go.

The recommended flow is:

1. prepare the TrueNAS Apps pool and RunnerHub datasets;
2. provide the GitHub App key manually or through Scale Secrets;
3. run the read-only host preflight;
4. verify the four RunnerHub images you intend to deploy;
5. generate installation YAML from the checked-in template;
6. install it through **Custom App > Install via YAML**;
7. run the post-install verifier;
8. run a real `runs-on: runnerhub` workflow.

`deploy/truenas/compose.yaml` remains the canonical Compose template. The helper scripts make the installation reproducible and fail early on common configuration mistakes.

## Security model

RunnerHub's `agent` service mounts `/var/run/docker.sock` because it creates and deletes ephemeral GitHub Actions worker containers on the TrueNAS Docker host. Treat the agent as root-equivalent trusted infrastructure. Workflow job containers do **not** receive the host Docker socket.

Use this deployment only for trusted private repositories. Do not expose port `8787` directly to the public Internet. The default YAML serves HTTP on the LAN so the dashboard can be opened from another machine; the API still requires a RunnerHub credential.

The default pool uses `container-trusted`, which does not give workflows a Docker daemon. If a workflow needs Docker container actions, service containers, or a `container:` job, use `container-dind` as described below rather than mounting the TrueNAS Docker socket into the workflow runner.

The GitHub App private key is a Compose file secret. The generated YAML contains its **host path only**, never its contents. Only `init-secrets`, `control`, and `scaleset` receive that secret.

## 1. Prepare TrueNAS storage

Configure a TrueNAS Apps storage pool if one is not already configured.

Create dedicated RunnerHub application storage. The preferred layout is:

```text
/mnt/POOL/runnerhub
/mnt/POOL/runnerhub/data
/mnt/POOL/runnerhub/agent
/mnt/POOL/runnerhub/secrets
```

Use your actual pool name instead of `tank`. Create `runnerhub` and the three child datasets with the **Apps** preset where practical. If you use ordinary directories instead, make sure they are readable, writable, and traversable from applications.

Do not place RunnerHub state in an unrelated database/media dataset. The agent path is intentionally bind-mounted at the **same absolute path** inside the agent container and on TrueNAS because the host Docker daemon later mounts short-lived JIT and optional DinD state from that path into worker infrastructure.

## 2. Create and install the GitHub App

For the repository-scoped automatic runner path, create a GitHub App and install it only on the private repository RunnerHub should manage.

Repository permissions:

```text
Administration: Read & write
Metadata: Read-only
```

Record:

```text
GitHub App client ID       Iv1...
Installation ID            numeric ID
Repository URL             https://github.com/OWNER/REPOSITORY
Private key                 .pem file
```

RunnerHub does not require an inbound webhook for this deployment path.

### Option A: manual key file

Place the private key at:

```text
/mnt/POOL/runnerhub/secrets/github-app.pem
```

Restrict it to the owner:

```bash
sudo chmod 600 /mnt/POOL/runnerhub/secrets/github-app.pem
```

This remains the default when no external key path is supplied.

### Option B: Scale Secrets

Upload the `.pem` through Scale Secrets as a file secret, for example with the secret name:

```text
runnerhub_github_app
```

Keep the PEM at the host path managed by Scale Secrets instead of copying it into RunnerHub. A path might look like:

```text
/mnt/POOL/app-secrets/secrets/runnerhub_github_app
```

Use the actual path reported by your Scale Secrets installation. See `SCALE_SECRETS.md` for the complete upload and rotation flow.

## 3. Run the host preflight

Manual/default key:

```bash
sudo bash deploy/truenas/preflight-host.sh /mnt/POOL/runnerhub
```

Scale Secrets or another external key file:

```bash
sudo bash deploy/truenas/preflight-host.sh \
  /mnt/POOL/runnerhub \
  /mnt/POOL/app-secrets/secrets/runnerhub_github_app
```

This is diagnostic only; it does not change TrueNAS configuration. It checks:

- TrueNAS SCALE 24.10 or newer;
- the Apps/Docker service is `RUNNING`;
- an Apps storage pool is configured;
- `/var/run/docker.sock` exists;
- the RunnerHub data/agent/secrets paths exist, are accessible, and are not symlinks;
- the selected GitHub App key is a regular non-symlink file;
- manual keys are owner-only (`0400`/`0600`), or a mode-`0444` managed key is protected by a mode-`0700` parent directory;
- OpenSSL can parse the GitHub App private key.

See `PREFLIGHT.md` for details.

## 4. Configure GHCR access and verify the image set

RunnerHub uses four runtime images:

```text
ghcr.io/eutelic/runnerhub-control
ghcr.io/eutelic/runnerhub-agent
ghcr.io/eutelic/runnerhub-bootstrap
ghcr.io/eutelic/runnerhub-scaleset
```

If the packages are private, configure TrueNAS GHCR access with a pull-only credential. Do not put a registry token in RunnerHub's Compose YAML.

Before installing, verify the tag you intend to deploy:

```bash
bash deploy/truenas/check-images.sh --tag latest
```

For an immutable deployment or rollback candidate:

```bash
bash deploy/truenas/check-images.sh --tag sha-FULL_COMMIT_SHA
```

The check requires all four images to be readable and to publish both `linux/amd64` and `linux/arm64`.

## 5. Generate the TrueNAS YAML

Manual/default key:

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-client-id Iv1.EXAMPLE \
  --github-installation-id 12345678 \
  --github-url https://github.com/OWNER/REPOSITORY \
  --output runnerhub-truenas.yaml
```

Scale Secrets/external key:

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-client-id Iv1.EXAMPLE \
  --github-installation-id 12345678 \
  --github-url https://github.com/OWNER/REPOSITORY \
  --github-app-key-path /mnt/POOL/app-secrets/secrets/runnerhub_github_app \
  --output runnerhub-truenas.yaml
```

Defaults:

```text
Dashboard/API port       8787
Pool ID                  pool-docker
runs-on label            runnerhub
Maximum runners          2
Isolation                container-trusted
RunnerHub image tag      latest
```

RunnerHub's internal enrollment/API credentials are **not** placed in the YAML. On first boot, `init-secrets` validates the selected GitHub App key and creates four random 256-bit credentials in the RunnerHub secrets dataset:

```text
enrollment-token
agent-secret
admin-key
scaleset-key
```

Existing valid values are preserved across restart/update. Malformed, empty, or symlinked generated credential state fails closed rather than being silently replaced.

For an immutable release, add:

```bash
--runnerhub-tag sha-FULL_COMMIT_SHA
```

For trusted workflows requiring Docker container features, add:

```bash
--isolation container-dind
```

`prepare-compose.sh` can run `docker compose config` without opening the real private key: it lints a temporary copy of the YAML that points at harmless local validation content. See `PREPARE_COMPOSE.md` for all options.

## 6. Install in TrueNAS

In TrueNAS:

1. Go to **Apps > Discover Apps**.
2. Open the Custom App menu and choose **Install via YAML**.
3. Use `runnerhub` as the application name.
4. Paste the generated `runnerhub-truenas.yaml`.
5. Save.

The stack starts in dependency order:

1. `init-secrets` validates the selected GitHub App key and prepares persistent RunnerHub credentials.
2. `control` opens the RunnerHub API and persistent SQLite database.
3. `agent` verifies host Docker access and enrolls the NAS as a Docker host.
4. one-shot `bootstrap` waits for a healthy control/agent pair and creates or updates the repository pool.
5. `scaleset` authenticates to GitHub and opens the outbound scale-set message session.

The `scaleset` readiness healthcheck becomes healthy only after configuration, GitHub authentication, scale-set lookup/create, RunnerHub pool validation, active-worker recovery, and GitHub message-session creation succeed.

No inbound GitHub webhook or public RunnerHub endpoint is required.

## 7. Verify the installation

Manual/default key:

```bash
sudo bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

External/Scale Secrets key:

```bash
sudo RUNNERHUB_GITHUB_APP_KEY_PATH=/mnt/POOL/app-secrets/secrets/runnerhub_github_app \
  bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

The verifier checks:

- persistent generated-secret format, permissions, and anti-symlink rules;
- the configured GitHub App key exists as a regular file;
- control health;
- unauthenticated API rejection;
- admin/scaler authorization boundaries;
- an online Docker host;
- the expected `pool-docker` pool and `runnerhub` label;
- persisted agent identity;
- when the host Docker CLI is available, the exact scale-set listener is healthy;
- stale RunnerHub worker containers;
- orphan DinD sidecars.

For a DinD deployment, also assert the expected isolation:

```bash
sudo RUNNERHUB_VERIFY_ISOLATION=container-dind \
  bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

Combine `RUNNERHUB_VERIFY_ISOLATION` with `RUNNERHUB_GITHUB_APP_KEY_PATH` when both apply.

Retrieve the generated admin key locally on TrueNAS when needed for dashboard/API administration:

```bash
sudo cat /mnt/POOL/runnerhub/secrets/admin-key
```

Do not place it in deployment YAML or logs.

## 8. Run a real GitHub Actions smoke job

Open the dashboard on the LAN:

```text
http://TRUENAS_IP:8787
```

Then add or run a workflow job in the configured private repository:

```yaml
jobs:
  smoke:
    runs-on: runnerhub
    steps:
      - uses: actions/checkout@v6
      - run: echo "RunnerHub on TrueNAS works"
```

Expected lifecycle:

1. GitHub sends demand to the outbound scale-set listener.
2. RunnerHub creates an ephemeral Docker worker on TrueNAS.
3. The job runs.
4. RunnerHub removes the worker and exact GitHub registration after completion.

After the job completes, run `verify.sh` again and confirm there is no stale worker container.

## Docker workflow compatibility

GitHub self-hosted runners require Docker for Docker container actions, service containers, and `container:` jobs. RunnerHub deliberately does **not** solve this by mounting TrueNAS `/var/run/docker.sock` into the workflow runner.

For trusted repositories that need those features, generate YAML with:

```bash
--isolation container-dind
```

The generator changes both required settings together:

```yaml
pool-isolation: &pool-isolation container-dind
allow-privileged-workers: &allow-privileged-workers "true"
```

`container-dind` creates a private Docker-in-Docker daemon for each ephemeral RunnerHub worker. The workflow runner sees only that private daemon socket; it never receives the TrueNAS host Docker socket.

The DinD sidecar itself runs with Docker `--privileged`, so this remains a trusted-workload-only mode and is not equivalent to VM/microVM isolation.

You can also pin the DinD image:

```bash
--dind-image docker:28-dind
```

## Persistence and backup

Back up the RunnerHub paths together:

```text
data/
agent/
secrets/
```

They contain the control database, per-host agent credential, and RunnerHub-generated service credentials.

If you use the manual/default key flow, `secrets/` also contains `github-app.pem`. If you use Scale Secrets or another external secret manager, back up that secret manager's data/key path separately; RunnerHub intentionally does not duplicate the PEM into its own persistent secrets directory.

Do not delete or regenerate individual RunnerHub-generated secret files while the stack is running.

The control image uses a fixed non-root UID/GID and performs a narrow startup ownership repair for the configured SQLite database/WAL files. This makes restores and upgrades deterministic without recursively changing unrelated files in the dataset.

## Updating, key rotation, and rollback

The default template uses `:latest` plus `pull_policy: always`. For a safer deployment, pin an immutable tag when generating YAML:

```bash
--runnerhub-tag sha-FULL_COMMIT_SHA
```

Before changing the installed app, validate the candidate tag:

```bash
bash deploy/truenas/check-images.sh --tag sha-FULL_COMMIT_SHA
```

Rollback by generating/installing YAML using the previous known-good SHA. Persistent state is retained; all four RunnerHub binaries move together.

When Scale Secrets replaces the GitHub App key, recreate/redeploy RunnerHub so the Compose secret mount receives the replacement file. Then run `verify.sh` with `RUNNERHUB_GITHUB_APP_KEY_PATH` set to the same managed path. Revoking an old GitHub App key is a separate GitHub-side operation.

## Troubleshooting first boot

Check services in dependency order:

1. `init-secrets`: missing/invalid PEM, malformed persisted credential, or unwritable secrets dataset.
2. `control`: database/dataset permissions, invalid credentials, or a rejected privileged-worker configuration.
3. `agent`: Docker socket/daemon access, enrollment, or state-path problems.
4. `bootstrap`: no online Docker host, GitHub repository security validation failure, or pool configuration failure.
5. `scaleset`: GitHub App client/installation/key mismatch, runner-group/scale-set mismatch, RunnerHub pool mismatch, or GitHub message-session failure.

Useful checks for the manual flow:

```bash
sudo bash deploy/truenas/preflight-host.sh /mnt/POOL/runnerhub
sudo bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

For a managed key, pass/set the same external path in both checks.

For DinD job failures, inspect the per-worker `runnerhub-*-dind` sidecar. RunnerHub treats a missing/exited sidecar as worker failure and removes it with the worker during cleanup.

Because initialization is fail-closed, a lower service should not start successfully after a prerequisite has failed.

## TrueNAS provider vs running on TrueNAS

The separate `RUNNERHUB_PROVIDER=truenas` adapter uses host-side `midclt` to create TrueNAS Custom Apps. It is an experimental provider for a host-side agent and is **not** required to run RunnerHub itself on modern TrueNAS SCALE.

For the RunnerHub appliance installed as a TrueNAS Custom App, use `RUNNERHUB_PROVIDER=docker` as the checked-in template does.
