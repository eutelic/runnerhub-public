# TrueNAS pre-install preflight

Run the host preflight before installing the RunnerHub Custom App. It is read-only.

## Prepare

Create:

```text
/mnt/POOL/runnerhub/data
/mnt/POOL/runnerhub/agent
/mnt/POOL/runnerhub/secrets
```

Provide both GitHub App secrets:

```text
/mnt/POOL/runnerhub/secrets/github-app.pem
/mnt/POOL/runnerhub/secrets/github-client-secret
```

For locally managed files, use owner-only permissions such as `0600`.

Scale Secrets-managed files may be `0444` when protected by a `0700` parent directory.

## Run

Default paths:

```bash
sudo bash deploy/truenas/preflight-host.sh /mnt/POOL/runnerhub
```

External paths:

```bash
sudo bash deploy/truenas/preflight-host.sh \
  /mnt/POOL/runnerhub \
  /mnt/POOL/app-secrets/secrets/runnerhub_github_app \
  /mnt/POOL/app-secrets/secrets/runnerhub_github_client_secret
```

The preflight verifies:

- TrueNAS SCALE 24.10+;
- Apps/Docker service is running;
- an Apps pool is configured;
- Docker socket exists;
- RunnerHub data/agent/secrets paths are accessible and not symlinks;
- GitHub App private key is protected and parses with OpenSSL;
- GitHub OAuth client secret is present, non-empty, and protected.

## Verify images

```bash
bash deploy/truenas/check-images.sh --tag latest
```

Or pin a release SHA.

## Generate and install

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-app-id 123456789 \
  --github-client-id Iv1.EXAMPLE \
  --github-app-slug runnerhub-self-hosted-ci \
  --public-url http://TRUENAS_HOST:8787 \
  --output runnerhub-truenas.yaml
```

Install that YAML as a TrueNAS Custom App.

## Verify before connecting repositories

```bash
sudo bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

A fresh deployment can pass with zero pools and report that repository onboarding is pending. The dynamic scale-set manager is expected to be healthy before any repository is connected.

Then open the configured public URL, sign in with GitHub, install/select repositories, and click **Connect**.
