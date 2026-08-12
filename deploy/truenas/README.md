# Deploy RunnerHub on TrueNAS SCALE

This is the supported **image-based** deployment path for TrueNAS SCALE 24.10 or newer, where Apps are backed by Docker and Custom Apps can be installed from Docker Compose YAML.

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

## 3. Generate RunnerHub secrets

Generate four independent values:

```bash
openssl rand -hex 32  # enrollment token
openssl rand -hex 32  # agent secret
openssl rand -hex 32  # admin key
openssl rand -hex 32  # scale-set key
```

Keep the admin key. The dashboard asks for it when making API requests.

## 4. Edit the TrueNAS Compose template

Open `deploy/truenas/compose.yaml` and replace the values in the `x-runnerhub-config` block:

- `/mnt/POOL/runnerhub/...` with your real dataset paths
- `CHANGE_ME_ENROLLMENT_TOKEN`
- `CHANGE_ME_AGENT_SECRET`
- `CHANGE_ME_ADMIN_KEY`
- `CHANGE_ME_SCALESET_KEY`
- `Iv1.CHANGE_ME`
- `CHANGE_ME_INSTALLATION_ID`
- `https://github.com/OWNER/REPOSITORY`

The YAML anchors intentionally define each secret once and reuse it across the services.

Port `8787` is the default dashboard/API port on the NAS. Change the `published` value if that port is already used.

## 5. If the GHCR packages are private

RunnerHub currently lives in a private GitHub repository, so GHCR package visibility may require authentication unless the packages are explicitly made public.

In TrueNAS go to **Apps > Configuration > Sign-in to a Docker registry**, add `ghcr.io`, and provide GitHub credentials that can read the RunnerHub packages. Alternatively, make the published RunnerHub container packages public before installing.

## 6. Install in TrueNAS

In TrueNAS:

1. Configure an Apps pool if one is not already configured.
2. Go to **Apps > Discover Apps**.
3. Open the menu next to Custom App and choose **Install via YAML**.
4. Use `runnerhub` as the application name.
5. Paste the edited contents of `deploy/truenas/compose.yaml`.
6. Save.

The stack starts in this order:

1. `control` starts and opens its internal API.
2. `agent` connects through the Compose network and enrolls as a Docker host.
3. one-shot `bootstrap` waits for the agent, creates/updates the repository pool, then exits successfully.
4. `scaleset` starts only after bootstrap succeeds and opens the outbound GitHub message session.

No inbound GitHub webhook or public RunnerHub endpoint is required.

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

## Updating

The template uses `:latest` plus `pull_policy: always`. After new images are published, use the TrueNAS app update/redeploy flow to pull them. The SQLite control-plane database and agent credentials are stored in the dedicated dataset and survive container replacement.

For safer rollback during alpha testing, replace `:latest` in all four service images with the same immutable `sha-<commit>` tag published by the image workflow. Returning all four services to the previous SHA rolls the application binaries back while preserving state.

## TrueNAS provider vs running on TrueNAS

The separate `RUNNERHUB_PROVIDER=truenas` adapter uses host-side `midclt` to create TrueNAS Custom Apps. It is an experimental provider for a host-side agent and is **not** required to run RunnerHub on modern TrueNAS SCALE.

For a RunnerHub appliance installed as a TrueNAS Custom App, use `RUNNERHUB_PROVIDER=docker` as this template does. That matches the Docker-based Apps runtime introduced in TrueNAS SCALE 24.10.
