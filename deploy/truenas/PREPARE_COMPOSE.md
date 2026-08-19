# Prepare the TrueNAS Compose file

`deploy/truenas/compose.yaml` remains the canonical TrueNAS SCALE 24.10+ Custom App template. `prepare-compose.sh` turns that template into installation-ready YAML without placing RunnerHub internal credentials or a GHCR registry token in the generated file.

## Basic usage

From a RunnerHub checkout:

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-client-id Iv1.EXAMPLE \
  --github-installation-id 12345678 \
  --github-url https://github.com/OWNER/REPOSITORY \
  --output runnerhub-truenas.yaml
```

The generated file uses:

- `/mnt/POOL/runnerhub/data`
- `/mnt/POOL/runnerhub/agent`
- `/mnt/POOL/runnerhub/secrets`
- dashboard/API port `8787`
- two maximum concurrent runners
- `container-trusted` isolation
- the coherent `latest` RunnerHub image set

Create the three datasets and place `github-app.pem` in the secrets dataset before installing the generated YAML in TrueNAS.

## Pin a release or rollback

Pass the same immutable RunnerHub tag to all four services with one option:

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-client-id Iv1.EXAMPLE \
  --github-installation-id 12345678 \
  --github-url https://github.com/OWNER/REPOSITORY \
  --runnerhub-tag sha-FULL_COMMIT_SHA \
  --output runnerhub-truenas.yaml
```

This prevents a mixed-version control/agent/bootstrap/scale-set stack.

## Docker workflow compatibility

For trusted repositories that require Docker container actions, service containers, or `container:` jobs, use:

```bash
--isolation container-dind
```

The generator automatically changes both the pool isolation and `RUNNERHUB_ALLOW_PRIVILEGED_WORKERS` gate together. The DinD sidecar is privileged; do not use this mode for untrusted workflow code.

You can also pin the DinD image:

```bash
--dind-image docker:28-dind
```

## Other options

```text
--max-runners N
--port PORT
--runner-image IMAGE
--runnerhub-tag TAG
--dind-image IMAGE
--output FILE
```

Without `--output`, the generated Compose YAML is written to stdout.

The helper validates all required identifiers and paths, rejects unsupported isolation modes and invalid ports, removes all installation placeholders, verifies every requested rewrite actually applied, and runs `docker compose config` automatically when Docker Compose is available locally.
