#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
prepare="$script_dir/prepare-compose.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

trusted="$tmp/trusted.yaml"
dind="$tmp/dind.yaml"

bash -n "$prepare"

bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-client-id Iv1.test \
  --github-installation-id 12345678 \
  --github-url https://github.com/acme/private-repo \
  --output "$trusted"

grep -Fq '/mnt/tank/runnerhub/data' "$trusted"
grep -Fq '/mnt/tank/runnerhub/agent' "$trusted"
grep -Fq '/mnt/tank/runnerhub/secrets' "$trusted"
grep -Fq 'github-client-id: &github-client-id Iv1.test' "$trusted"
grep -Fq 'github-installation-id: &github-installation-id 12345678' "$trusted"
grep -Fq 'github-url: &github-url https://github.com/acme/private-repo' "$trusted"
grep -Fq 'max-runners: &max-runners "2"' "$trusted"
grep -Fq 'published: "8787"' "$trusted"
grep -Fq 'pool-isolation: &pool-isolation container-trusted' "$trusted"
grep -Fq 'allow-privileged-workers: &allow-privileged-workers "false"' "$trusted"
grep -Fq 'ghcr.io/example-user/runnerhub-control:latest' "$trusted"
grep -Fq 'io.runnerhub.component: scaleset' "$trusted"
grep -Fq 'io.runnerhub.pool: *pool-id' "$trusted"
grep -Fq 'io.runnerhub.scale-set: *scaleset-name' "$trusted"
grep -Fq 'test -s /run/runnerhub/scaleset-ready' "$trusted"
! grep -Eq 'CHANGE_ME|/mnt/POOL/runnerhub|github\.com/OWNER/REPOSITORY' "$trusted"

bash "$prepare" \
  --root /mnt/fast/apps/runnerhub \
  --github-client-id Iv1.dind \
  --github-installation-id 87654321 \
  --github-url https://github.com/acme/docker-heavy \
  --max-runners 4 \
  --port 9876 \
  --isolation container-dind \
  --runnerhub-tag sha-deadbeef \
  --runner-image ghcr.io/actions/actions-runner:2.328.0 \
  --dind-image docker:28-dind \
  --output "$dind"

grep -Fq 'max-runners: &max-runners "4"' "$dind"
grep -Fq 'published: "9876"' "$dind"
grep -Fq 'pool-isolation: &pool-isolation container-dind' "$dind"
grep -Fq 'allow-privileged-workers: &allow-privileged-workers "true"' "$dind"
grep -Fq 'runner-image: &runner-image ghcr.io/actions/actions-runner:2.328.0' "$dind"
grep -Fq 'dind-image: &dind-image docker:28-dind' "$dind"
for image in control agent bootstrap scaleset; do
  grep -Fq "ghcr.io/example-user/runnerhub-${image}:sha-deadbeef" "$dind"
done

if bash "$prepare" \
  --root /srv/runnerhub \
  --github-client-id Iv1.test \
  --github-installation-id 1 \
  --github-url https://github.com/acme/repo >/dev/null 2>&1; then
  echo 'invalid non-/mnt TrueNAS root unexpectedly succeeded' >&2
  exit 1
fi

if bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-client-id Iv1.test \
  --github-installation-id not-a-number \
  --github-url https://github.com/acme/repo >/dev/null 2>&1; then
  echo 'invalid installation ID unexpectedly succeeded' >&2
  exit 1
fi

if bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-client-id Iv1.test \
  --github-installation-id 1 \
  --github-url https://example.com/acme/repo >/dev/null 2>&1; then
  echo 'non-GitHub repository URL unexpectedly succeeded' >&2
  exit 1
fi

if bash "$prepare" \
  --root /mnt/tank/runnerhub \
  --github-client-id Iv1.test \
  --github-installation-id 1 \
  --github-url https://github.com/acme/repo \
  --port 70000 >/dev/null 2>&1; then
  echo 'invalid dashboard port unexpectedly succeeded' >&2
  exit 1
fi

printf 'TrueNAS compose preparation tests passed.\n'
