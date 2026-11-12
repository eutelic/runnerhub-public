#!/usr/bin/env bash
# Verify digest-pinned agent/update-helper images signed by the trusted
# RunnerHub publish-images workflow using GitHub OIDC + keyless Cosign.
# This is a release preflight; it does not authorize host mutation.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash deploy/truenas/verify-agent-update-attestations.sh SOURCE_SHA \
    ghcr.io/eutelic/runnerhub-agent@sha256:AGENT_DIGEST \
    ghcr.io/eutelic/runnerhub-agent-updater@sha256:HELPER_DIGEST

Requires Cosign v3, Python 3, and GHCR read access (if packages are private).
Checks the exact digest, trusted GitHub OIDC workflow identity, source SHA
certificate claim, and signed source-sha annotation for both images.
BuildKit provenance and SBOM remain embedded in the image manifest.
This check does NOT update hosts.
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

command -v cosign >/dev/null || { echo "[fail] Cosign is required" >&2; exit 2; }
command -v python3 >/dev/null || { echo "[fail] Python 3 is required" >&2; exit 2; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
chmod 0700 "$tmp"

# Anchor the workflow path and allow only the release workflow's supported
# trigger refs. This is verified against the Fulcio certificate SAN, not
# untrusted OCI labels or caller-provided release names.
signer='^https://github\.com/eutelic/runnerhub/\.github/workflows/publish-images\.yml@refs/(heads/main|tags/v[^/[:space:]]+)$'

for artifact in "$agent" "$helper"; do
  # Cosign verifies the signature against the digest-pinned ref, Fulcio OIDC
  # certificate, workflow identity/repo/SHA, signed annotation and Rekor log.
  # Never bypass transparency verification or accept other workflow identities.
  if ! cosign verify \
    --certificate-identity-regexp "$signer" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    --certificate-github-workflow-repository "eutelic/runnerhub" \
    --certificate-github-workflow-sha "$revision" \
    --annotations "source-sha=$revision" \
    --output json "$artifact" > "$tmp/proof.json"; then
    echo "[fail] signed release image verification failed for $artifact" >&2
    exit 1
  fi

  # Independent exact-subject validation protects against a verifier output
  # shape change silently accepting an unexpected subject or missing SHA.
  python3 - "$tmp/proof.json" "$artifact" "$revision" <<'PY'
import json
import sys

proof_path, image_ref, revision = sys.argv[1:]
_, sep, digest = image_ref.partition("@sha256:")
if sep != "@sha256:":
    raise SystemExit("[fail] expected digest-pinned image")

with open(proof_path, encoding="utf-8") as f:
    records = json.load(f)
if not isinstance(records, list) or not records:
    raise SystemExit("[fail] no verified signatures returned")

for record in records:
    critical = record.get("critical") or record.get("Critical") or {}
    image = critical.get("image") or critical.get("Image") or {}
    observed = image.get("docker-manifest-digest") or image.get("Docker-manifest-digest")
    optional = record.get("optional") or record.get("Optional") or {}
    if observed == "sha256:" + digest and optional.get("source-sha") == revision:
        break
else:
    raise SystemExit("[fail] verified signature does not bind the requested digest and source SHA")
PY

  echo "[ok] signed RunnerHub release verified for $artifact"
done

echo "[ok] both images verified at source revision $revision; no host mutation performed"
