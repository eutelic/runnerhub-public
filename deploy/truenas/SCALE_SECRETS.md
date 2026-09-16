# Use Scale Secrets with RunnerHub

RunnerHub can consume both GitHub App secrets directly from Scale Secrets:

```text
runnerhub_github_app
runnerhub_github_client_secret
```

The first contains the GitHub App private-key PEM. The second contains the GitHub App OAuth client secret.

## Preflight

Use the actual paths reported by Scale Secrets:

```bash
sudo bash deploy/truenas/preflight-host.sh \
  /mnt/POOL/runnerhub \
  /mnt/POOL/app-secrets/secrets/runnerhub_github_app \
  /mnt/POOL/app-secrets/secrets/runnerhub_github_client_secret
```

RunnerHub accepts Scale Secrets' mode-`0444` consumer files when the containing secrets directory is mode `0700`.

## Generate YAML

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

The generated YAML contains only those host paths. RunnerHub's generated enrollment/admin/scaler credentials remain in `<root>/secrets`.

Repository installation IDs and repository URLs are not deployment inputs. Select repositories later through the RunnerHub GitHub onboarding UI.

## Rotation

Replace the relevant file through Scale Secrets, then recreate/redeploy RunnerHub so Compose receives the replacement file object.

Verify using the same external paths:

```bash
sudo RUNNERHUB_GITHUB_APP_KEY_PATH=/mnt/POOL/app-secrets/secrets/runnerhub_github_app \
  RUNNERHUB_GITHUB_CLIENT_SECRET_PATH=/mnt/POOL/app-secrets/secrets/runnerhub_github_client_secret \
  bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

Revoking old keys/client secrets in GitHub is a separate GitHub-side operation.
