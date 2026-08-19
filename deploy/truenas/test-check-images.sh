#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
check="$script_dir/check-images.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"

cat > "$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[ "${1:-}" = manifest ] || exit 9
[ "${2:-}" = inspect ] || exit 9
ref="${3:-}"

case "${FAKE_IMAGE_MODE:-ok}" in
  missing)
    if [[ "$ref" == *runnerhub-agent:* ]]; then
      echo 'manifest unknown' >&2
      exit 1
    fi
    ;;
  badarch)
    if [[ "$ref" == *runnerhub-scaleset:* ]]; then
      cat <<'JSON'
{"schemaVersion":2,"manifests":[{"platform":{"os":"linux","architecture":"amd64"}}]}
JSON
      exit 0
    fi
    ;;
esac

cat <<'JSON'
{"schemaVersion":2,"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}},{"platform":{"os":"unknown","architecture":"unknown"}}]}
JSON
EOF
chmod +x "$tmp/bin/docker"

PATH="$tmp/bin:$PATH" bash -n "$check"
PATH="$tmp/bin:$PATH" bash "$check" --tag sha-deadbeef > "$tmp/success.log"
for image in control agent bootstrap scaleset; do
  grep -Fq "ghcr.io/example-user/runnerhub-$image:sha-deadbeef publishes linux/amd64 and linux/arm64" "$tmp/success.log"
done
grep -Fq '[ ok ] all four RunnerHub runtime images are available for tag sha-deadbeef' "$tmp/success.log"

if FAKE_IMAGE_MODE=missing PATH="$tmp/bin:$PATH" bash "$check" --tag sha-deadbeef >/dev/null 2>&1; then
  echo 'image preflight unexpectedly accepted a missing image' >&2
  exit 1
fi

if FAKE_IMAGE_MODE=badarch PATH="$tmp/bin:$PATH" bash "$check" --tag sha-deadbeef >/dev/null 2>&1; then
  echo 'image preflight unexpectedly accepted a manifest missing linux/arm64' >&2
  exit 1
fi

if PATH="$tmp/bin:$PATH" bash "$check" --tag 'bad tag' >/dev/null 2>&1; then
  echo 'image preflight unexpectedly accepted an invalid tag' >&2
  exit 1
fi

printf 'TrueNAS release image preflight tests passed.\n'
