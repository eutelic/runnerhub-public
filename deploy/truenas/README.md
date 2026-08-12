# Deploy RunnerHub on TrueNAS SCALE

This is the supported **image-based** deployment path for TrueNAS SCALE 24.10 or newer, where Custom Apps can be installed from Docker Compose YAML.

It does not build RunnerHub on the NAS. TrueNAS pulls the prebuilt RunnerHub control, agent, bootstrap, and scale-set images from GHCR.

## Security model

RunnerHub's `agent` service mounts `/var/run/docker.sock` because it creates and deletes ephemeral GitHub Actions worker containers on the TrueNAS Docker host. Treat the agent as root-equivalent trusted infrastructure. Workflow job containers do **not** receive the Docker socket.

Use this deployment only for trusted private repositories. Do not expose port `8787` directly to the public Internet. The default YAML serves HTTP on the LAN so the dashboard can be opened from another machine; the API still requires a RunnerHub credential.

## 1. Prepare storage

Create a dedicated dataset, for example:

```text
/mnt/POOL/runnerhub
```

Create these paths inside it:

```text
/mnt/POOL/runnerhub/data
/mnt/POOL/runnerhub/agent
/mnt/POOL/runnerhub/secrets
```

Copy the GitHub App private key to:

```text
/mnt/POOL/runnerhub/secrets/github-app.pem
```

Keep the PEM file private (mode `0600` is recommended).

Do not place RunnerHub state in an unrelated database/media dataset. The agent path is intentionally bind-mounted at the **same absolute path** inside the agent container and on TrueNAS because the host Docker daemon later mounts short-lived JIT files from that path into worker containers.

## 2. Create the GitHub App

For the repository-scoped automatic runner path, create a GitHub App and install it only on the private repository RunnerHub should manage.

Record:

- GitHub App client ID (`Iv1...`)
- installation ID
- repository URL (`https://github.com/OWNER/REPOSITORY`)
- the PEM private key copied above

The current quickstart permission set is repository **Administration: read/write** plus **Metadata: read-only**.

## 3. Edit the TrueNAS Compose template

Open `deploy/truenas/compose.yaml` and change only the installation-specific values in `x-runnerhub-config`:

- `/mnt/POOL/runnerhub/...` to your real dataset paths
- `Iv1.CHANGE_ME` to the GitHub App client ID
- `CHANGE_ME_INSTALLATION_ID` to the installation ID
- `https://github.com/OWNER/REPOSITORY` to the target private repository

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

## 4. If the GHCR packages are private

If the RunnerHub GHCR packages are private, TrueNAS must authenticate to `ghcr.io` with credentials that can read the packages. Configure this under the Apps Docker registry settings before installing. If the packages are public, no registry credential is needed.

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
3. `agent` connects through the Compose network and enrolls as a Docker host.
4. one-shot `bootstrap` waits for the agent and creates/updates the repository pool.
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

The template uses `:latest` plus `pull_policy: always`. After new images are published, use the TrueNAS app update/redeploy flow to pull them. Persistent state stays in the dedicated dataset and survives container replacement.

For safer rollback during alpha testing, replace `:latest` in all four RunnerHub service images with the same immutable `sha-<commit>` tag published by the image workflow. Returning all four services to the previous SHA rolls application binaries back while preserving state.

The `init-secrets` service also uses the bootstrap image; pin it to the same SHA as the bootstrap service when pinning a deployment.

## Troubleshooting first boot

Check services in dependency order:

1. `init-secrets`: invalid/missing PEM or unwritable secrets dataset.
2. `control`: database/dataset permissions or invalid generated credential files.
3. `agent`: Docker socket access, state path, or enrollment failure.
4. `bootstrap`: no online Docker host or repository pool validation failure.
5. `scaleset`: GitHub App permissions/installation ID or GitHub message-session failure.

Because initialization is fail-closed, a lower service should not start successfully after a prerequisite has failed.

## TrueNAS provider vs running on TrueNAS

The separate `RUNNERHUB_PROVIDER=truenas` adapter uses host-side `midclt` to create TrueNAS Custom Apps. It is an experimental provider for a host-side agent and is **not** required to run RunnerHub on modern TrueNAS SCALE.

For a RunnerHub appliance installed as a TrueNAS Custom App, use `RUNNERHUB_PROVIDER=docker` as this template does.
