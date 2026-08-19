# Deploy RunnerHub on TrueNAS SCALE

This is the supported **image-based** deployment path for TrueNAS SCALE 24.10 or newer. TrueNAS pulls prebuilt RunnerHub images from GHCR; the NAS does not need to build RunnerHub or install Go.

The recommended flow is:

1. prepare the TrueNAS Apps pool, RunnerHub datasets, and GitHub App key;
2. run the read-only host preflight;
3. verify that the four RunnerHub images you intend to deploy are pullable;
4. generate installation YAML from the checked-in template;
5. install it through **Custom App > Install via YAML**;
6. run the post-install verifier;
7. run a real `runs-on: runnerhub` workflow.

`deploy/truenas/compose.yaml` remains the canonical Compose template. The helper scripts make the installation reproducible and fail early on common configuration mistakes.

## Security model

RunnerHub's `agent` service mounts `/var/run/docker.sock` because it creates and deletes ephemeral GitHub Actions worker containers on the TrueNAS Docker host. Treat the agent as root-equivalent trusted infrastructure. Workflow job containers do **not** receive the host Docker socket.

Use this deployment only for trusted private repositories. Do not expose port `8787` directly to the public Internet. The default YAML serves HTTP on the LAN so the dashboard can be opened from another machine; the API still requires a RunnerHub credential.

The default pool uses `container-trusted`, which does not give workflows a Docker daemon. If a workflow needs Docker container actions, service containers, or a `container:` job, see **Docker workflow compatibility** below rather than mounting the TrueNAS Docker socket into the workflow runner.

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

Copy the private key to:

```text
/mnt/POOL/runnerhub/secrets/github-app.pem
```

Restrict it to the owner:

```bash
sudo chmod 600 /mnt/POOL/runnerhub/secrets/github-app.pem
```

RunnerHub does not require an inbound webhook for this deployment path.

## 3. Run the host preflight

From a RunnerHub checkout on the TrueNAS shell:

```bash
sudo bash deploy/truenas/preflight-host.sh /mnt/POOL/runnerhub
```

This is diagnostic only; it does not change TrueNAS configuration. It checks:

- TrueNAS SCALE 24.10 or newer;
- the Apps/Docker service is `RUNNING`;
- an Apps storage pool is configured;
- `/var/run/docker.sock` exists;
- the RunnerHub data/agent/secrets paths exist, are accessible, and are not symlinks;
- `github-app.pem` is owner-only and parses as a private key.

See `deploy/truenas/PREFLIGHT.md` for the detailed pre-install flow.

## 4. Configure GHCR access and verify the image set

RunnerHub uses four runtime images:

```text
ghcr.io/example-user/runnerhub-control
ghcr.io/example-user/runnerhub-agent
ghcr.io/example-user/runnerhub-bootstrap
ghcr.io/example-user/runnerhub-scaleset
```

If the packages are private, TrueNAS must authenticate to GHCR. Create a **personal access token (classic)** with only `read:packages`, then in TrueNAS use **Apps > Configuration > Sign-in to a Docker registry** and add GHCR:

```text
Registry: https://ghcr.io
Username: your GitHub username
Password: PAT classic with read:packages
```

Do not put the registry token in RunnerHub's Compose YAML.

Before installing, verify the image tag from a machine whose Docker client has the same GHCR access:

```bash
bash deploy/truenas/check-images.sh --tag latest
```

For an immutable deployment or rollback candidate:

```bash
bash deploy/truenas/check-images.sh --tag sha-FULL_COMMIT_SHA
```

The preflight requires all four images to be readable and to publish both `linux/amd64` and `linux/arm64`.

## 5. Generate the TrueNAS YAML

Use the deterministic generator rather than hand-editing the template:

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-client-id Iv1.EXAMPLE \
  --github-installation-id 12345678 \
  --github-url https://github.com/OWNER/REPOSITORY \
  --output runnerhub-truenas.yaml
```

The generated YAML defaults to:

```text
Dashboard/API port       8787
Pool ID                  pool-docker
runs-on label            runnerhub
Maximum runners          2
Isolation                container-trusted
RunnerHub image tag      latest
```

RunnerHub's internal enrollment/API credentials are **not** placed in the YAML. On first boot, `init-secrets` validates `github-app.pem` and creates four random 256-bit credentials in the secrets dataset:

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

That applies the same tag to control, agent, bootstrap, and scale-set images.

See `deploy/truenas/PREPARE_COMPOSE.md` for all generator options.

### Manual template editing

If you deliberately prefer to edit `deploy/truenas/compose.yaml` yourself, change only the installation-specific values in `x-runnerhub-config`:

- `/mnt/POOL/runnerhub/...`
- `Iv1.CHANGE_ME`
- `CHANGE_ME_INSTALLATION_ID`
- `https://github.com/OWNER/REPOSITORY`

Keep the four RunnerHub service images on the same tag. Do not independently pin or update only one RunnerHub service.

## 6. Install in TrueNAS

In TrueNAS:

1. Go to **Apps > Discover Apps**.
2. Open the Custom App menu and choose **Install via YAML**.
3. Use `runnerhub` as the application name.
4. Paste the generated `runnerhub-truenas.yaml`.
5. Save.

The stack starts in dependency order:

1. `init-secrets` validates the GitHub App key and prepares persistent RunnerHub credentials.
2. `control` opens the RunnerHub API and persistent SQLite database.
3. `agent` verifies host Docker access and enrolls the NAS as a Docker host.
4. one-shot `bootstrap` waits for a healthy control/agent pair and creates or updates the repository pool.
5. `scaleset` authenticates to GitHub and opens the outbound scale-set message session.

The `scaleset` process has a dedicated readiness healthcheck. It becomes healthy only after configuration, GitHub authentication, scale-set lookup/create, RunnerHub pool validation, active-worker recovery, GitHub message-session creation, and listener construction all succeed. A stale readiness marker is removed on every process restart before setup is repeated.

No inbound GitHub webhook or public RunnerHub endpoint is required.

## 7. Verify the installation

Run the post-install verifier from a checkout on the TrueNAS shell:

```bash
sudo bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

The verifier checks:

- persistent generated-secret format, permissions, and anti-symlink rules;
- GitHub App key presence;
- control health;
- unauthenticated API rejection;
- admin/scaler authorization boundaries;
- an online Docker host;
- the expected `pool-docker` pool and `runnerhub` label;
- persisted agent identity;
- when the host Docker CLI is available, the exact scale-set listener is `healthy` after GitHub message-session setup;
- stale RunnerHub worker containers;
- orphan DinD sidecars.

If the host Docker CLI is unavailable, the direct container checks are reported as skipped while the API, state, credential, host, and pool checks still run.

For a DinD deployment, also assert the expected isolation:

```bash
sudo RUNNERHUB_VERIFY_ISOLATION=container-dind \
  bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

Retrieve the generated admin key when you need dashboard/API administration:

```bash
sudo cat /mnt/POOL/runnerhub/secrets/admin-key
```

Store it in a password manager.

## 8. Run a real GitHub Actions smoke job

Open the dashboard:

```text
http://TRUENAS_IP:8787
```

Then add a workflow job to the configured private repository:

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

After the job completes, run `verify.sh` again and confirm the worker reaches `deleted` in RunnerHub with no stale worker container remaining.

## Docker workflow compatibility

GitHub self-hosted runners require Docker for Docker container actions, service containers, and `container:` jobs. RunnerHub deliberately does **not** solve this by mounting TrueNAS `/var/run/docker.sock` into the workflow runner.

For trusted repositories that need those features, generate the YAML with:

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

DinD currently has coarser resource accounting than `container-trusted`: pool CPU/memory limits apply to the runner container, but the privileged DinD sidecar and nested containers do not yet share one scheduler-accounted CPU/memory/disk envelope.

## Persistence and backup

Back up all three RunnerHub paths together:

```text
data/
agent/
secrets/
```

They contain the control database, per-host agent credential, generated service credentials, and GitHub App key needed to restore the same installation.

Do not delete or regenerate individual secret files while the stack is running.

The control image uses a fixed non-root UID/GID and performs a narrow startup ownership repair for the configured SQLite database/WAL files. This makes restores and upgrades from older image identities deterministic without recursively changing unrelated files in the dataset.

## Updating and rollback

The default template uses `:latest` plus `pull_policy: always`. Release publishing creates immutable same-commit tags first and advances `latest` only after all four runtime images build successfully. The promotion step then verifies that `latest` contains the exact `linux/amd64` and `linux/arm64` child manifests from that source commit.

For a safer alpha deployment, pin an immutable tag when generating the YAML:

```bash
--runnerhub-tag sha-FULL_COMMIT_SHA
```

Before changing the installed app, validate the candidate tag:

```bash
bash deploy/truenas/check-images.sh --tag sha-FULL_COMMIT_SHA
```

Rollback by generating/installing YAML using the previous known-good SHA. Persistent state is retained; all four RunnerHub binaries move together.

## Troubleshooting first boot

Check services in dependency order:

1. `init-secrets`: missing/invalid PEM, malformed persisted credential, or unwritable secrets dataset.
2. `control`: database/dataset permissions, invalid credentials, or a rejected privileged-worker configuration.
3. `agent`: Docker socket/daemon access, enrollment, or state-path problems.
4. `bootstrap`: no online Docker host, GitHub repository security validation failure, or pool configuration failure.
5. `scaleset`: GitHub App client/installation/key mismatch, runner-group/scale-set mismatch, RunnerHub pool mismatch, or GitHub message-session failure. The container remains unhealthy until the full listener setup reaches its readiness boundary.

Useful checks:

```bash
sudo bash deploy/truenas/preflight-host.sh /mnt/POOL/runnerhub
sudo bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

For DinD job failures, inspect the per-worker `runnerhub-*-dind` sidecar. RunnerHub treats a missing/exited sidecar as worker failure and removes it with the worker during cleanup.

Because initialization is fail-closed, a lower service should not start successfully after a prerequisite has failed.

## TrueNAS provider vs running on TrueNAS

The separate `RUNNERHUB_PROVIDER=truenas` adapter uses host-side `midclt` to create TrueNAS Custom Apps. It is an experimental provider for a host-side agent and is **not** required to run RunnerHub itself on modern TrueNAS SCALE.

For the RunnerHub appliance installed as a TrueNAS Custom App, use `RUNNERHUB_PROVIDER=docker` as the checked-in template does.
