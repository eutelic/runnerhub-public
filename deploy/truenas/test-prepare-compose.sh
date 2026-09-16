#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
prepare="$script_dir/prepare-compose.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

trusted="$tmp/trusted.yaml"
dind="$tmp/dind.yaml"
managed="$tmp/managed.yaml"

bash -n "$prepare"

bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-app-id 42424242 \
  --github-client-id Iv1.test \
  --github-app-slug runnerhub-test \
  --public-url http://runnerhub.example.test:8787 \
  --output "$trusted"

grep -Fq '/mnt/tank/runnerhub/data' "$trusted"
grep -Fq '/mnt/tank/runnerhub/agent' "$trusted"
grep -Fq '/mnt/tank/runnerhub/secrets' "$trusted"
grep -Fq 'github-app-key-path: &github-app-key-path /mnt/tank/runnerhub/secrets/github-app.pem' "$trusted"
grep -Fq 'github-client-secret-path: &github-client-secret-path /mnt/tank/runnerhub/secrets/github-client-secret' "$trusted"
grep -Fq 'file: *github-app-key-path' "$trusted"
grep -Fq 'file: *github-client-secret-path' "$trusted"
grep -Fq 'RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE: /run/secrets/runnerhub-github-app' "$trusted"
grep -Fq 'RUNNERHUB_GITHUB_CLIENT_SECRET_FILE: /run/secrets/runnerhub-github-client-secret' "$trusted"
grep -Fq 'github-app-id: &github-app-id 42424242' "$trusted"
grep -Fq 'github-client-id: &github-client-id Iv1.test' "$trusted"
grep -Fq 'github-app-slug: &github-app-slug runnerhub-test' "$trusted"
grep -Fq 'public-url: &public-url http://runnerhub.example.test:8787' "$trusted"
grep -Fq 'published: "8787"' "$trusted"
grep -Fq 'pool-isolation: &pool-isolation container-trusted' "$trusted"
grep -Fq 'allow-privileged-workers: &allow-privileged-workers "false"' "$trusted"
grep -Fq 'ghcr.io/eutelic/runnerhub-control:latest' "$trusted"
grep -Fq 'io.runnerhub.component: scaleset' "$trusted"
grep -Fq 'RUNNERHUB_DYNAMIC_CONFIG: "true"' "$trusted"
grep -Fq 'test -s /run/runnerhub/scaleset-ready' "$trusted"
! grep -Eq 'CHANGE_ME|TRUENAS_HOST|/mnt/POOL/runnerhub|github-installation-id|github-url:' "$trusted"

bash "$prepare" \
  --root /mnt/fast/apps/runnerhub \
  --github-app-id 42424242 \
  --github-client-id Iv1.dind \
  --github-app-slug runnerhub-dind \
  --public-url https://runnerhub.example.com \
  --port 9876 \
  --isolation container-dind \
  --runnerhub-tag sha-deadbeef \
  --runner-image ghcr.io/actions/actions-runner:2.328.0 \
  --dind-image docker:28-dind \
  --output "$dind"

grep -Fq 'published: "9876"' "$dind"
grep -Fq 'pool-isolation: &pool-isolation container-dind' "$dind"
grep -Fq 'allow-privileged-workers: &allow-privileged-workers "true"' "$dind"
grep -Fq 'runner-image: &runner-image ghcr.io/actions/actions-runner:2.328.0' "$dind"
grep -Fq 'dind-image: &dind-image docker:28-dind' "$dind"
grep -Fq 'public-url: &public-url https://runnerhub.example.com' "$dind"
for image in control agent bootstrap scaleset; do
  grep -Fq "ghcr.io/eutelic/runnerhub-${image}:sha-deadbeef" "$dind"
done

managed_key='/mnt/Databases/apps_n_stuff/scale-secrets/secrets/runnerhub_github_app'
managed_client_secret='/mnt/Databases/apps_n_stuff/scale-secrets/secrets/runnerhub_github_client_secret'
bash "$prepare" \
  --root /mnt/Databases/apps_n_stuff/runnerhub \
  --github-app-id 42424242 \
  --github-client-id Iv1.managed \
  --github-app-slug runnerhub-managed \
  --public-url http://TRUENAS_HOST:8787 \
  --github-app-key-path "$managed_key" \
  --github-client-secret-path "$managed_client_secret" \
  --output "$managed"

grep -Fq "github-app-key-path: &github-app-key-path $managed_key" "$managed"
grep -Fq "github-client-secret-path: &github-client-secret-path $managed_client_secret" "$managed"
grep -Fq 'secrets-path: &secrets-path /mnt/Databases/apps_n_stuff/runnerhub/secrets' "$managed"
! grep -Fq 'runnerhub/secrets/github-app.pem' "$managed"
! grep -Fq 'runnerhub/secrets/github-client-secret' "$managed"


if bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-app-id not-a-number \
  --github-client-id Iv1.test \
  --github-app-slug runnerhub-test \
  --public-url http://localhost:8787 >/dev/null 2>&1; then
  echo 'invalid GitHub App ID unexpectedly succeeded' >&2
  exit 1
fi

if bash "$prepare" \
  --root /srv/runnerhub \
  --github-app-id 42424242 \
  --github-client-id Iv1.test \
  --github-app-slug runnerhub-test \
  --public-url http://localhost:8787 >/dev/null 2>&1; then
  echo 'invalid non-/mnt TrueNAS root unexpectedly succeeded' >&2
  exit 1
fi

if bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-app-id 42424242 \
  --github-client-id Iv1.test \
  --github-app-slug 'bad slug' \
  --public-url http://localhost:8787 >/dev/null 2>&1; then
  echo 'invalid GitHub App slug unexpectedly succeeded' >&2
  exit 1
fi

if bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-app-id 42424242 \
  --github-client-id Iv1.test \
  --github-app-slug runnerhub-test \
  --public-url ftp://runnerhub.example.com >/dev/null 2>&1; then
  echo 'invalid public URL unexpectedly succeeded' >&2
  exit 1
fi

if bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-app-id 42424242 \
  --github-client-id Iv1.test \
  --github-app-slug runnerhub-test \
  --public-url http://localhost:8787 \
  --github-app-key-path /srv/not-truenas/key.pem >/dev/null 2>&1; then
  echo 'non-/mnt GitHub App key path unexpectedly succeeded' >&2
  exit 1
fi

if bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-app-id 42424242 \
  --github-client-id Iv1.test \
  --github-app-slug runnerhub-test \
  --public-url http://localhost:8787 \
  --github-client-secret-path /srv/not-truenas/client-secret >/dev/null 2>&1; then
  echo 'non-/mnt GitHub client secret path unexpectedly succeeded' >&2
  exit 1
fi

if bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-app-id 42424242 \
  --github-client-id Iv1.test \
  --github-app-slug runnerhub-test \
  --public-url http://localhost:8787 \
  --port 70000 >/dev/null 2>&1; then
  echo 'invalid dashboard port unexpectedly succeeded' >&2
  exit 1
fi

printf 'TrueNAS compose preparation tests passed.\n'
