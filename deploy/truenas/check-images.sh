#!/usr/bin/env bash
set -euo pipefail

registry="${RUNNERHUB_IMAGE_REGISTRY:-ghcr.io/eutelic}"
tag="${RUNNERHUB_IMAGE_TAG:-latest}"

usage() {
  cat <<'EOF'
Usage:
  bash deploy/truenas/check-images.sh [--tag TAG] [--registry REGISTRY]

Checks that the four RunnerHub runtime images for the selected tag are readable
with the current Docker registry credentials and publish both linux/amd64 and
linux/arm64 manifests.

Examples:
  bash deploy/truenas/check-images.sh
  bash deploy/truenas/check-images.sh --tag sha-FULL_COMMIT_SHA
EOF
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

ok() {
  printf '[ ok ] %s\n' "$*"
}

while (($#)); do
  case "$1" in
    --tag)
      (($# >= 2)) || fail '--tag requires a value'
      tag="$2"
      shift 2
      ;;
    --registry)
      (($# >= 2)) || fail '--registry requires a value'
      registry="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

[[ "$tag" =~ ^[A-Za-z0-9_.-]+$ ]] || fail 'image tag contains unsupported characters'
[[ "$registry" =~ ^[A-Za-z0-9._:/-]+$ ]] || fail 'registry prefix contains unsupported characters'
registry="${registry%/}"

for cmd in docker python3; do
  command -v "$cmd" >/dev/null 2>&1 || fail "$cmd is required"
done

images=(control agent bootstrap scaleset)
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
chmod 0700 "$tmp"

for image in "${images[@]}"; do
  ref="$registry/runnerhub-$image:$tag"
  manifest="$tmp/$image.json"

  if ! docker manifest inspect "$ref" > "$manifest" 2> "$tmp/$image.err"; then
    detail="$(tr '\n' ' ' < "$tmp/$image.err" | sed 's/[[:space:]]\+/ /g' | cut -c1-300)"
    fail "cannot inspect $ref. Check that the tag exists and that Docker is authenticated to the registry.${detail:+ Detail: $detail}"
  fi

  python3 - "$manifest" "$ref" <<'PY'
import json
import sys

path, ref = sys.argv[1:]
with open(path) as f:
    doc = json.load(f)

manifests = doc.get("manifests")
if not isinstance(manifests, list):
    raise SystemExit(f"[fail] {ref} is not a multi-platform manifest list")

platforms = {
    (m.get("platform") or {}).get("os", "") + "/" + (m.get("platform") or {}).get("architecture", "")
    for m in manifests
}
required = {"linux/amd64", "linux/arm64"}
missing = sorted(required - platforms)
if missing:
    raise SystemExit(f"[fail] {ref} is missing required platforms: {', '.join(missing)}")

print(f"[ ok ] {ref} publishes linux/amd64 and linux/arm64")
PY
done

ok "all four RunnerHub runtime images are available for tag $tag"
