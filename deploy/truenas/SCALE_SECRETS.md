# Use Scale Secrets for the GitHub App key

RunnerHub can consume its GitHub App private key directly from a file managed by Scale Secrets. The private-key bytes are not copied into RunnerHub's YAML or its own secrets directory.

The normal/manual deployment remains supported: if `--github-app-key-path` is omitted, RunnerHub uses `<root>/secrets/github-app.pem` exactly as before.

## 1. Upload the key to Scale Secrets

In the Scale Secrets UI, create a secret named, for example:

```text
runnerhub_github_app
```

Choose the file-upload path and select the GitHub App `.pem` file. Scale Secrets preserves the uploaded bytes exactly and does not display them again.

After creation, use the host file path shown by Scale Secrets for that secret. A typical path has this shape:

```text
/mnt/POOL/app-secrets/secrets/runnerhub_github_app
```

Use the path reported by your installation; do not assume the example path.

## 2. Run RunnerHub preflight against that file

```bash
sudo bash deploy/truenas/preflight-host.sh \
  /mnt/POOL/runnerhub \
  /mnt/POOL/app-secrets/secrets/runnerhub_github_app
```

The preflight parses the key with OpenSSL and accepts either the normal owner-only RunnerHub key mode (`0400`/`0600`) or Scale Secrets' `0444` consumer-file mode when the file is protected by its mode-`0700` secrets directory.

## 3. Generate the TrueNAS YAML

Pass the same host path to the generator:

```bash
bash deploy/truenas/prepare-compose.sh \
  --root /mnt/POOL/runnerhub \
  --github-client-id Iv1.EXAMPLE \
  --github-installation-id 12345678 \
  --github-url https://github.com/OWNER/REPOSITORY \
  --github-app-key-path /mnt/POOL/app-secrets/secrets/runnerhub_github_app \
  --output runnerhub-truenas.yaml
```

The generated Compose declares that host file as the `runnerhub-github-app` Compose secret and grants it only to the RunnerHub services that need the GitHub App key (`init-secrets`, `control`, and `scaleset`). RunnerHub's generated enrollment, agent, admin, and scale-set credentials remain in `<root>/secrets`.

`prepare-compose.sh` validates a copy of the generated Compose file against a harmless temporary secret. It does not ask Docker Compose to open the real GitHub App private key merely to lint the YAML.

## Rotation

Replace the file through Scale Secrets, then recreate/redeploy RunnerHub so the services receive the new file object. Scale Secrets uses atomic replacement, and a running bind-backed Compose secret can otherwise remain attached to the previous inode.

After redeploying, run the verifier with the same external key path:

```bash
sudo RUNNERHUB_GITHUB_APP_KEY_PATH=/mnt/POOL/app-secrets/secrets/runnerhub_github_app \
  bash deploy/truenas/verify.sh /mnt/POOL/runnerhub http://127.0.0.1:8787
```

If the old GitHub App key is being revoked, complete that rotation in GitHub as well. Replacing the local file alone does not revoke an issuer-side credential.
