# Prepare the TrueNAS Compose file

`deploy/truenas/compose.yaml` is the canonical TrueNAS SCALE 24.10+ template. The generator produces installation-ready YAML without embedding RunnerHub internal credentials, GitHub App private-key bytes, the OAuth client secret, or a GHCR token.

## Basic usage

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-client-id Iv1.EXAMPLE \
  --github-app-slug runnerhub-self-hosted-ci \
  --public-url http://TRUENAS_HOST:8787 \
  --output runnerhub-truenas.yaml
```

Repository access is intentionally **not** configured here. After deployment, sign in to RunnerHub, install/manage the GitHub App, select repositories, and click **Connect**.

Default secret files:

```text
/mnt/POOL/runnerhub/secrets/github-app.pem
/mnt/POOL/runnerhub/secrets/github-client-secret
```

## External/Scale Secrets files

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-client-id Iv1.EXAMPLE \
  --github-app-slug runnerhub-self-hosted-ci \
  --public-url http://TRUENAS_HOST:8787 \
  --github-app-key-path /mnt/POOL/app-secrets/secrets/runnerhub_github_app \
  --github-client-secret-path /mnt/POOL/app-secrets/secrets/runnerhub_github_client_secret \
  --output runnerhub-truenas.yaml
```

The generated YAML stores only the host paths.

## Release pinning

```bash
--runnerhub-tag sha-FULL_COMMIT_SHA
```

The same tag is applied to control, agent, bootstrap, and scale-set images.

## Docker compatibility

Default:

```text
--isolation container-trusted
```

For trusted workflows that require Docker actions/services/job containers:

```text
--isolation container-dind
```

The generator enables the required privileged-worker gate at the same time.

## Options

```text
--root PATH
--github-client-id ID
--github-app-slug SLUG
--public-url URL
--github-app-key-path /mnt/.../FILE
--github-client-secret-path /mnt/.../FILE
--port PORT
--isolation container-trusted|container-dind
--runnerhub-tag TAG
--runner-image IMAGE
--dind-image IMAGE
--output FILE
```

There is no `--github-installation-id` or `--github-url` in the normal TrueNAS flow. Those values are discovered after GitHub browser onboarding.

When Docker Compose is available locally, the generator validates a temporary copy of the generated YAML using harmless temporary secret files.
