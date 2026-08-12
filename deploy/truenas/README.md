# Deploy RunnerHub on TrueNAS SCALE

This is the supported **image-based** deployment path for TrueNAS SCALE 24.10 or newer, where Custom Apps can be installed from Docker Compose YAML.

It does not build RunnerHub on the NAS. TrueNAS pulls the prebuilt RunnerHub control, agent, bootstrap, and scale-set images from GHCR.

## Security model

RunnerHub's `agent` service mounts `/var/run/docker.sock` because it creates and deletes ephemeral GitHub Actions worker containers on the TrueNAS Docker host. Treat the agent as root-equivalent trusted infrastructure. Workflow job containers do **not** receive the host Docker socket.

Use this deployment only for trusted private repositories. Do not expose port `8787` directly to the public Internet. The default YAML serves HTTP on the LAN so the dashboard can be opened from another machine; the API still requires a RunnerHub credential.

The default pool uses `container-trusted`, which does not give workflows a Docker daemon. If a workflow needs Docker container actions, service containers, or a `container:` job, see **Docker workflow compatibility** below rather than mounting the TrueNAS Docker socket into the job.

## 1. Prepare storage

Create dedicated RunnerHub application storage. TrueNAS recommends separate datasets for application data and the **Apps** dataset preset for application storage, so the preferred layout is:

```text
/mnt/POOL/runnerhub
/mnt/POOL/runnerhub/data
/mnt/POOL/runnerhub/agent
/mnt/POOL/runnerhub/secrets
```

Create `runnerhub` and the three child datasets with the **Apps** preset where practical. If you use ordinary directories instead, ensure the Docker app can read/write them and preserve execute/traverse permission on the parent path.

Copy the GitHub App private key to:

```text
/mnt/POOL/runnerhub/secrets/github-app.pem
```

Keep the PEM file private (mode `0600` is recommended).

Do not place RunnerHub state in an unrelated database/media dataset. The agent path is intentionally bind-mounted at the **same absolute path** inside the agent container and on TrueNAS because the host Docker daemon later mounts short-lived JIT and optional DinD state from that path into worker infrastructure.

## 2. Create the GitHub App

For the repository-scoped automatic runner path, create a GitHub App and install it only on the private repository RunnerHub should manage.

Record:

- GitHub App client ID (`Iv1...`)
- installation ID
- repository URL (`https://github.com/OWNER/REPOSITORY`)
- the PEM private key copied above

The current quickstart permission set is repository **Administration: read/write** plus **Metadata: read-only**.

## 3. Edit the TrueNAS Compose template

Open `deploy/truenas/compose.yaml` and change the installation-specific values in `x-runnerhub-config`:

- `/mnt/POOL/runnerhub/...` to your real dataset paths
- `Iv1.CHANGE_ME` to the GitHub App client ID
- `CHANGE_ME_INSTALLATION_ID` to the installation ID
- `https://github.com/OWNER/REPOSITORY` to the target private repository

Keep these safe defaults unless you specifically need Docker-based workflow features:

```yaml
pool-isolation: &pool-isolation container-trusted
allow-privileged-workers: &allow-privileged-workers "false"
```

You do **not** need to generate or paste RunnerHub enrollment/API secrets into the YAML.

On first boot, the `init-secrets` service validates `github-app.pem` and creates four random 256-bit credentials in the secrets dataset:

```text
enrollment-token
agent-secret
admin-key
scaleset-key
```

Existing non-empty secret files are preserved, so restarting or updating the app does not rotate credentials unexpectedly. An empty secret file causes startup to fail instead of silently replacing it.

Port `8787` is the default dashboard/API port on the NAS. Change the `published` value if that port is already used.

## 4. Configure GHCR access

GitHub Container Registry packages are private when first published unless their visibility is changed. If the four RunnerHub packages are still private, TrueNAS must authenticate before the app can pull them.

Create a **personal access token (classic)** with only `read:packages` for this pull-only use. In TrueNAS go to **Apps > Configuration > Sign-in to a Docker registry** and add an **Other Registry** entry for GHCR using:

```text
Registry: https://ghcr.io
Username: your GitHub username
Password: the PAT classic with read:packages
```

Do not put this registry token in the RunnerHub Compose YAML.

Alternatively, the four RunnerHub container packages can be changed to **Public** in GitHub package settings. Public GHCR containers support anonymous pulls and therefore remove the registry credential from the TrueNAS setup. GitHub treats changing a package to public as irreversible, so RunnerHub does not automate that decision.

## 5. Install in TrueNAS

In TrueNAS:

1. Configure an Apps pool if one is not already configured.
2. Go to **Apps > Discover Apps**.
3. Open the menu next to Custom App and choose **Install via YAML**.
4. Use `runnerhub` as the application name.
5. Paste the edited contents of `deploy/truenas/compose.yaml`.
6. Save.

The stack starts in this order:

1. `init-secrets` validates the GitHub App key and prepares persistent RunnerHub credentials.
2. `control` starts and opens its internal API.
3. `agent` verifies access to the host Docker daemon, connects through the Compose network, and enrolls as a Docker host.
4. one-shot `bootstrap` starts only after the control plane and Docker daemon checks are healthy, then creates/updates the repository pool.
5. `scaleset` starts only after bootstrap succeeds and opens the outbound GitHub message session.

No inbound GitHub webhook or public RunnerHub endpoint is required.

## 6. Retrieve the admin key

After the first successful start, read the generated admin key from the TrueNAS shell:

```bash
sudo cat /mnt/POOL/runnerhub/secrets/admin-key
```

Use your actual dataset path. Store this value in your password manager; it authorizes administrative RunnerHub API operations.

## 7. Verify

Open:

```text
http://TRUENAS_IP:8787
```

Paste the admin key into the API token field. The host should show `online` with provider `docker`.

Then add a workflow job to the configured repository:

```yaml
jobs:
  smoke:
    runs-on: runnerhub
    steps:
      - uses: actions/checkout@v6
      - run: echo "RunnerHub on TrueNAS works"
```

After the job completes, verify that the worker reaches `deleted` in RunnerHub and that no stale `runnerhub-*` worker container remains.

## Docker workflow compatibility

GitHub self-hosted runners require Docker for Docker container actions, service containers, and `container:` jobs. RunnerHub deliberately does **not** solve this by mounting TrueNAS `/var/run/docker.sock` into the workflow runner.

If the configured repository requires those features, change both TrueNAS template values:

```yaml
pool-isolation: &pool-isolation container-dind
allow-privileged-workers: &allow-privileged-workers "true"
```

`container-dind` creates a private Docker-in-Docker daemon for each ephemeral RunnerHub worker. The runner sees only that private daemon socket; it never receives the TrueNAS host Docker socket.

The tradeoff is that the DinD sidecar itself runs with Docker `--privileged`, which is a materially weaker host-isolation boundary than the default mode. Use it only for trusted private-repository CI. It is **not** equivalent to a VM/microVM sandbox.

The infrastructure image can also be pinned in the template:

```yaml
dind-image: &dind-image docker:dind
```

For a production deployment, pin the DinD image to a tested version/digest as part of the same upgrade discipline as RunnerHub itself.

DinD work/daemon state is currently bind-backed and does not enforce the same per-worker tmpfs byte ceiling as `container-trusted`; strict DinD disk quota enforcement remains a hardening item.

## Persistence and backup

Back up all three RunnerHub paths together:

```text
data/
agent/
secrets/
```

The control database, per-host agent credential, generated service credentials, and GitHub App key are all required to restore the same installation cleanly.

Do not delete or regenerate individual secret files while the stack is running.

## Updating and rollback

The template uses `:latest` plus `pull_policy: always`. Image publishing first creates immutable tags for the same source commit across every RunnerHub service, then advances the `latest` tags only after all four builds succeed. This reduces the chance of an update pulling a mixed-version RunnerHub stack.

For the safest alpha rollback, replace `:latest` in all four RunnerHub service images with the same immutable `sha-<full-commit>` tag published by the image workflow. Returning all four services to the previous SHA rolls application binaries back while preserving state.

The `init-secrets` service also uses the bootstrap image; pin it to the same SHA as the bootstrap service when pinning a deployment.

## Troubleshooting first boot

Check services in dependency order:

1. `init-secrets`: invalid/missing PEM or unwritable secrets dataset.
2. `control`: database/dataset permissions, invalid generated credential files, or a rejected privileged-worker configuration.
3. `agent`: its Docker healthcheck fails when `/var/run/docker.sock` is unavailable or the Docker daemon cannot be reached; enrollment/state-path problems then appear in agent logs.
4. `bootstrap`: no online Docker host or repository pool validation failure. A `container-dind` pool is rejected unless privileged workers are explicitly enabled.
5. `scaleset`: GitHub App permissions/installation ID or GitHub message-session failure.

For DinD job failures, also inspect the per-worker `runnerhub-*-dind` sidecar. RunnerHub treats a missing/exited DinD sidecar as a worker failure and removes it with the worker during cleanup.

Because initialization is fail-closed, a lower service should not start successfully after a prerequisite has failed.

## TrueNAS provider vs running on TrueNAS

The separate `RUNNERHUB_PROVIDER=truenas` adapter uses host-side `midclt` to create TrueNAS Custom Apps. It is an experimental provider for a host-side agent and is **not** required to run RunnerHub on modern TrueNAS SCALE.

For a RunnerHub appliance installed as a TrueNAS Custom App, use `RUNNERHUB_PROVIDER=docker` as this template does.
