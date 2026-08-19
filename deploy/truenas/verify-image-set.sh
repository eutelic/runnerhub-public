#!/usr/bin/env bash
set -euo pipefail

registry="${RUNNERHUB_IMAGE_REGISTRY:-ghcr.io/example-user}"
source_tag="${RUNNERHUB_IMAGE_SOURCE_TAG:-}"
target_tag="${RUNNERHUB_IMAGE_TARGET_TAG:-latest}"

usage() {
  cat <<'EOF'
Usage:
  bash deploy/truenas/verify-image-set.sh \
    --source-tag sha-FULL_COMMIT_SHA \
    [--target-tag latest] \
    [--registry ghcr.io/example-user]

Verifies that all four target-tag RunnerHub images publish linux/amd64 and
linux/arm64 using the exact same platform manifest digests as the source tag.
This is intended to verify coherent registry-side promotion and rollback sets.
EOF
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

while (($#)); do
  case "$1" in
    --source-tag)
      (($# >= 2)) || fail '--source-tag requires a value'
      source_tag="$2"
      shift 2
      ;;
    --target-tag)
      (($# >= 2)) || fail '--target-tag requires a value'
      target_tag="$2"
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

[[ -n "$source_tag" ]] || fail '--source-tag is required'
[[ "$source_tag" =~ ^[A-Za-z0-9_.-]+$ ]] || fail 'source tag contains unsupported characters'
[[ "$target_tag" =~ ^[A-Za-z0-9_.-]+$ ]] || fail 'target tag contains unsupported characters'
[[ "$registry" =~ ^[A-Za-z0-9._:/-]+$ ]] || fail 'registry prefix contains unsupported characters'
registry="${registry%/}"

for cmd in docker python3; do
  command -v "$cmd" >/dev/null 2>&1 || fail "$cmd is required"
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
chmod 0700 "$tmp"

for image in control agent bootstrap scaleset; do
  source_ref="$registry/runnerhub-$image:$source_tag"
  target_ref="$registry/runnerhub-$image:$target_tag"
  source_json="$tmp/$image-source.json"
  target_json="$tmp/$image-target.json"

  if ! docker buildx imagetools inspect --raw "$source_ref" > "$source_json" 2> "$tmp/$image-source.err"; then
    detail="$(tr '\n' ' ' < "$tmp/$image-source.err" | sed 's/[[:space:]]\+/ /g' | cut -c1-300)"
    fail "cannot inspect source image $source_ref.${detail:+ Detail: $detail}"
  fi
  if ! docker buildx imagetools inspect --raw "$target_ref" > "$target_json" 2> "$tmp/$image-target.err"; then
    detail="$(tr '\n' ' ' < "$tmp/$image-target.err" | sed 's/[[:space:]]\+/ /g' | cut -c1-300)"
    fail "cannot inspect target image $target_ref.${detail:+ Detail: $detail}"
  fi

  python3 - "$source_json" "$target_json" "$source_ref" "$target_ref" <<'PY'
import json
import sys

source_path, target_path, source_ref, target_ref = sys.argv[1:]
required = {"linux/amd64", "linux/arm64"}


def platform_digests(path, ref):
    with open(path) as f:
        doc = json.load(f)
    manifests = doc.get("manifests")
    if not isinstance(manifests, list):
        raise SystemExit(f"[fail] {ref} is not a multi-platform manifest list")

    result = {}
    for manifest in manifests:
        platform = manifest.get("platform") or {}
        key = f"{platform.get('os', '')}/{platform.get('architecture', '')}"
        if key not in required:
            continue
        digest = manifest.get("digest")
        if not isinstance(digest, str) or not digest.startswith("sha256:"):
            raise SystemExit(f"[fail] {ref} has invalid digest for {key}: {digest!r}")
        if key in result:
            raise SystemExit(f"[fail] {ref} contains duplicate manifest entries for {key}")
        result[key] = digest

    missing = sorted(required - set(result))
    if missing:
        raise SystemExit(f"[fail] {ref} is missing required platforms: {', '.join(missing)}")
    return result


source = platform_digests(source_path, source_ref)
target = platform_digests(target_path, target_ref)
if source != target:
    mismatches = [
        f"{platform}: source={source.get(platform)} target={target.get(platform)}"
        for platform in sorted(required)
        if source.get(platform) != target.get(platform)
    ]
    raise SystemExit(
        f"[fail] {target_ref} does not match {source_ref}: " + "; ".join(mismatches)
    )

print(f"[ ok ] {target_ref} matches {source_ref} for linux/amd64 and linux/arm64")
PY
done

printf '[ ok ] coherent RunnerHub image promotion verified: %s -> %s\n' "$source_tag" "$target_tag"
