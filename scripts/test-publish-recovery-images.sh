#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
publisher="$repo_root/scripts/publish-recovery-images.sh"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/runnerhub-recovery-cli.XXXXXX")"
trap 'rm -rf -- "$tmp"' EXIT

bash "$publisher" --help >"$tmp/help" 2>&1
grep -Fq 'Builds and publishes all six RunnerHub runtime images' "$tmp/help"

if bash "$publisher" --version 1.2.3 >"$tmp/invalid.out" 2>"$tmp/invalid.err"; then
  echo "publisher accepted a version without the v prefix" >&2
  exit 1
fi
grep -Fq 'a version tag like v1.2.3 is required' "$tmp/invalid.err"

if bash "$publisher" --unexpected >"$tmp/unknown.out" 2>"$tmp/unknown.err"; then
  echo "publisher accepted an unknown option" >&2
  exit 1
fi
grep -Fq 'unknown argument: --unexpected' "$tmp/unknown.err"

if env -u RUNNERHUB_RECOVERY_GHCR_USERNAME \
  -u RUNNERHUB_RECOVERY_GHCR_TOKEN \
  -u RUNNERHUB_RECOVERY_SIGNING_KEY \
  bash "$publisher" --version v1.2.3 >"$tmp/credentials.out" 2>"$tmp/credentials.err"; then
  echo "publisher continued without GHCR credentials" >&2
  exit 1
fi
grep -Fq 'RUNNERHUB_RECOVERY_GHCR_USERNAME is required' "$tmp/credentials.err"

echo "[ ok ] recovery publisher CLI rejects unsafe or incomplete invocation"

python3 -m unittest discover -s "$repo_root/scripts" -p test_recovery_publisher.py
