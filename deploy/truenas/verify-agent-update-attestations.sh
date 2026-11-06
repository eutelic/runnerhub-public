#!/usr/bin/env bash
# Verify the two pinned update images against signed GitHub SLSA provenance.
# This is an operator preflight, not a replacement for Docker host drill tests.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash deploy/truenas/verify-agent-update-attestations.sh SOURCE_SHA \
    ghcr.io/eutelic/runnerhub-agent@sha256:AGENT_DIGEST \
    ghcr.io/eutelic/runnerhub-agent-updater@sha256:HELPER_DIGEST

Requires an authenticated GitHub CLI (gh), Python 3, and GHCR credentials.
A passing result proves the exact digests were attested by the trusted
RunnerHub image publishing workflow at SOURCE_SHA. It does NOT update hosts.
EOF
}

if [ "$#" -ne 3 ]; then
  usage >&2
  exit 2
fi

revision="$1"
agent="$2"
helper="$3"

[[ "$revision" =~ ^[a-f0-9]{40}$ ]] || { echo "[fail] expected full 40-character source commit" >&2; exit 2; }
[[ "$agent" =~ ^ghcr\.io/eutelic/runnerhub-agent@sha256:[a-f0-9]{64}$ ]] || { echo "[fail] invalid pinned agent image" >&2; exit 2; }
[[ "$helper" =~ ^ghcr\.io/eutelic/runnerhub-agent-updater@sha256:[a-f0-9]{64}$ ]] || { echo "[fail] invalid pinned updater image" >&2; exit 2; }

command -v gh >/dev/null || { echo "[fail] GitHub CLI is required" >&2; exit 2; }
command -v python3 >/dev/null || { echo "[fail] Python 3 is required" >&2; exit 2; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
chmod 0700 "$tmp"

for artifact in "$agent" "$helper"; do
  # The source digest and signer workflow are enforced using the signed
  # GitHub OIDC certificate, not untrusted OCI labels or workflow output.
  # Do NOT add --bundle-from-oci here: querying GitHub's API also verifies
  # the current repository identity instead of a potentially renamed name.
  if ! gh attestation verify "oci://$artifact" \
    --repo eutelic/runnerhub \
    --signer-workflow eutelic/runnerhub/.github/workflows/publish-images.yml \
    --source-digest "$revision" \
    --predicate-type https://slsa.dev/provenance/v1 \
    --format json > "$tmp/proof.json"; then
    echo "[fail] signed release provenance verification failed" >&2
    exit 1
  fi

  python3 - "$tmp/proof.json" "$artifact" <<'PY'
import json
import sys

proof_path, image_ref = sys.argv[1:]
image_name, sep, digest = image_ref.partition("@sha256:")
if sep != "@sha256:":
    raise SystemExit("[fail] image is not digest pinned")

with open(proof_path, encoding="utf-8") as f:
    records = json.load(f)
if not isinstance(records, list) or not records:
    raise SystemExit("[fail] signed provenance returned no verified records")

matched = False
for record in records:
    statement = (record.get("verificationResult") or {}).get("statement") or {}
    if statement.get("predicateType") != "https://slsa.dev/provenance/v1":
        continue
    for subject in statement.get("subject") or []:
        if subject.get("name") == image_name and (subject.get("digest") or {}).get("sha256") == digest:
            matched = True
if not matched:
    raise SystemExit("[fail] verified statement does not bind the exact requested image digest")
PY

  echo "[ok] signed RunnerHub publication provenance verified for $artifact"
done

echo "[ok] both images verified at source revision $revision; no host mutation performed"
