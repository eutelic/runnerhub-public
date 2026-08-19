# TrueNAS pre-install preflight

Run the host preflight **before** installing the RunnerHub Custom App. It is read-only: it checks TrueNAS and filesystem state but does not configure the Apps service, create datasets, change permissions, or install anything.

## 1. Prepare TrueNAS in the WebUI

Before running the script:

1. Use TrueNAS SCALE 24.10 or newer.
2. Configure an Apps storage pool and confirm the Apps service is running.
3. Create the RunnerHub storage paths, preferably as dedicated datasets:

   ```text
   /mnt/POOL/runnerhub/data
   /mnt/POOL/runnerhub/agent
   /mnt/POOL/runnerhub/secrets
   ```

4. Copy the GitHub App private key to:

   ```text
   /mnt/POOL/runnerhub/secrets/github-app.pem
   ```

5. Restrict the key to the owner, for example:

   ```bash
   chmod 600 /mnt/POOL/runnerhub/secrets/github-app.pem
   ```

Use your actual pool/path instead of `tank`.

## 2. Run the host preflight

From a RunnerHub checkout on the TrueNAS shell:

```bash
sudo bash deploy/truenas/preflight-host.sh /mnt/POOL/runnerhub
```

The preflight verifies:

- the installed TrueNAS release is 24.10 or newer;
- the TrueNAS Apps/Docker service reports `RUNNING`;
- an Apps storage pool is configured;
- `/var/run/docker.sock` exists as a Unix socket;
- the `data`, `agent`, and `secrets` paths exist, are not symlinks, and are accessible;
- `github-app.pem` is a regular non-symlink file with owner-only permissions;
- OpenSSL can parse the GitHub App private key.

A host Docker CLI is optional. If it exists, the script checks that it can reach the daemon; RunnerHub itself does not require the host CLI because the agent image carries its own Docker client.

## 3. Verify the RunnerHub image set

Before pasting the Custom App YAML, verify that the four images for the tag you intend to deploy are readable and publish both supported architectures.

If the GHCR packages are private, authenticate Docker to GHCR first with a pull-only credential. Then run:

```bash
bash deploy/truenas/check-images.sh --tag latest
```

For an immutable deployment or rollback:

```bash
bash deploy/truenas/check-images.sh --tag sha-FULL_COMMIT_SHA
```

This catches missing/private packages and incomplete multi-architecture releases before TrueNAS starts the app.

## 4. Generate the installation YAML

Use the deterministic generator described in `PREPARE_COMPOSE.md`:

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-client-id Iv1.EXAMPLE \
  --github-installation-id 12345678 \
  --github-url https://github.com/OWNER/REPOSITORY \
  --output runnerhub-truenas.yaml
```

Use `--runnerhub-tag sha-FULL_COMMIT_SHA` if you want the generated installation to use an immutable image set instead of `latest`.

## 5. Install and verify

Paste the generated YAML into **Apps > Discover Apps > Custom App > Install via YAML** and install it as `runnerhub`.

After the stack is up, run:

```bash
sudo bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

The post-install verifier defaults to pool ID `pool-docker` and runner label `runnerhub`. For a DinD deployment, also assert the expected isolation:

```bash
sudo RUNNERHUB_VERIFY_ISOLATION=container-dind \
  bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```
