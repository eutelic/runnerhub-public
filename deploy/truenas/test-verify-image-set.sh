#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
verify="$script_dir/verify-image-set.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"

cat > "$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[ "${1:-}" = buildx ] || exit 9
[ "${2:-}" = imagetools ] || exit 9
[ "${3:-}" = inspect ] || exit 9
[ "${4:-}" = --raw ] || exit 9
ref="${5:-}"

image=""
case "$ref" in
  *runnerhub-control:*) image=control ;;
  *runnerhub-agent:*) image=agent ;;
  *runnerhub-bootstrap:*) image=bootstrap ;;
  *runnerhub-scaleset:*) image=scaleset ;;
  *runnerhub-deployer:*) image=deployer ;;
  *runnerhub-agent-updater:*) image=agent-updater ;;
  *) echo 'unknown image' >&2; exit 1 ;;
esac

tag="${ref##*:}"
mode="${FAKE_PROMOTION_MODE:-ok}"

if [ "$mode" = missing ] && [ "$image" = agent ] && [ "$tag" = latest ]; then
  echo 'manifest unknown' >&2
  exit 1
fi

repeat_hex() {
  printf '%064s' '' | tr ' ' "$1"
}

case "$image" in
  control) amd_char=a; arm_char=b ;;
  agent) amd_char=c; arm_char=d ;;
  bootstrap) amd_char=e; arm_char=f ;;
  scaleset) amd_char=1; arm_char=2 ;;
  deployer) amd_char=4; arm_char=5 ;;
  agent-updater) amd_char=6; arm_char=7 ;;
esac

amd="sha256:$(repeat_hex "$amd_char")"
arm="sha256:$(repeat_hex "$arm_char")"

if [ "$mode" = mismatch ] && [ "$image" = bootstrap ] && [ "$tag" = latest ]; then
  arm="sha256:$(repeat_hex 3)"
fi

if [ "$mode" = baddigest ] && [ "$image" = agent ] && [ "$tag" = latest ]; then
  arm='sha256:not-a-real-digest'
fi

if [ "$mode" = badarch ] && [ "$image" = scaleset ] && [ "$tag" = latest ]; then
  printf '{"schemaVersion":2,"manifests":[{"digest":"%s","platform":{"os":"linux","architecture":"amd64"}}]}\n' "$amd"
  exit 0
fi

if [ "$mode" = duplicate ] && [ "$image" = control ] && [ "$tag" = latest ]; then
  printf '{"schemaVersion":2,"manifests":[{"digest":"%s","platform":{"os":"linux","architecture":"amd64"}},{"digest":"%s","platform":{"os":"linux","architecture":"amd64"}},{"digest":"%s","platform":{"os":"linux","architecture":"arm64"}}]}\n' "$amd" "$amd" "$arm"
  exit 0
fi

printf '{"schemaVersion":2,"manifests":[{"digest":"%s","platform":{"os":"linux","architecture":"amd64"}},{"digest":"%s","platform":{"os":"linux","architecture":"arm64"}},{"digest":"sha256:attestation","platform":{"os":"unknown","architecture":"unknown"}}]}\n' "$amd" "$arm"
EOF
chmod +x "$tmp/bin/docker"

PATH="$tmp/bin:$PATH" bash -n "$verify"
PATH="$tmp/bin:$PATH" bash "$verify" --source-tag sha-deadbeef --target-tag latest > "$tmp/success.log"
for image in control agent bootstrap scaleset deployer agent-updater; do
  grep -Fq "ghcr.io/eutelic/runnerhub-$image:latest matches ghcr.io/eutelic/runnerhub-$image:sha-deadbeef" "$tmp/success.log"
done
grep -Fq '[ ok ] coherent RunnerHub image promotion verified: sha-deadbeef -> latest' "$tmp/success.log"

if FAKE_PROMOTION_MODE=mismatch PATH="$tmp/bin:$PATH" bash "$verify" --source-tag sha-deadbeef >/dev/null 2>&1; then
  echo 'promotion verifier unexpectedly accepted a platform digest mismatch' >&2
  exit 1
fi

if FAKE_PROMOTION_MODE=missing PATH="$tmp/bin:$PATH" bash "$verify" --source-tag sha-deadbeef >/dev/null 2>&1; then
  echo 'promotion verifier unexpectedly accepted a missing target image' >&2
  exit 1
fi

if FAKE_PROMOTION_MODE=badarch PATH="$tmp/bin:$PATH" bash "$verify" --source-tag sha-deadbeef >/dev/null 2>&1; then
  echo 'promotion verifier unexpectedly accepted a missing target platform' >&2
  exit 1
fi

if FAKE_PROMOTION_MODE=duplicate PATH="$tmp/bin:$PATH" bash "$verify" --source-tag sha-deadbeef >/dev/null 2>&1; then
  echo 'promotion verifier unexpectedly accepted duplicate platform manifests' >&2
  exit 1
fi

if FAKE_PROMOTION_MODE=baddigest PATH="$tmp/bin:$PATH" bash "$verify" --source-tag sha-deadbeef >/dev/null 2>&1; then
  echo 'promotion verifier unexpectedly accepted an invalid SHA-256 digest' >&2
  exit 1
fi

if PATH="$tmp/bin:$PATH" bash "$verify" --source-tag 'bad tag' >/dev/null 2>&1; then
  echo 'promotion verifier unexpectedly accepted an invalid source tag' >&2
  exit 1
fi

printf 'RunnerHub image promotion verification tests passed.\n'
