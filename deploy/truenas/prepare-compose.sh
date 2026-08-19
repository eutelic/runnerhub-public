#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash deploy/truenas/prepare-compose.sh \
    --root /mnt/POOL/runnerhub \
    --github-client-id Iv1.EXAMPLE \
    --github-installation-id 12345678 \
    --github-url https://github.com/OWNER/REPOSITORY \
    [--max-runners 2] \
    [--port 8787] \
    [--isolation container-trusted|container-dind] \
    [--runnerhub-tag latest|sha-...] \
    [--runner-image IMAGE] \
    [--dind-image IMAGE] \
    [--output FILE]

The generated YAML contains installation identifiers and paths, but no RunnerHub
internal credentials or GitHub registry token. RunnerHub generates its internal
credentials on first boot in the configured secrets dataset.
EOF
}

fail() {
  printf 'prepare-compose: %s\n' "$*" >&2
  exit 1
}

escape_sed_replacement() {
  printf '%s' "$1" | sed 's/[\\&|]/\\&/g'
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
template="$script_dir/compose.yaml"

root=""
github_client_id=""
github_installation_id=""
github_url=""
max_runners="2"
port="8787"
isolation="container-trusted"
runnerhub_tag="latest"
runner_image="ghcr.io/actions/actions-runner:latest"
dind_image="docker:dind"
output=""

while (($#)); do
  case "$1" in
    --root)
      (($# >= 2)) || fail '--root requires a value'
      root="$2"
      shift 2
      ;;
    --github-client-id)
      (($# >= 2)) || fail '--github-client-id requires a value'
      github_client_id="$2"
      shift 2
      ;;
    --github-installation-id)
      (($# >= 2)) || fail '--github-installation-id requires a value'
      github_installation_id="$2"
      shift 2
      ;;
    --github-url)
      (($# >= 2)) || fail '--github-url requires a value'
      github_url="$2"
      shift 2
      ;;
    --max-runners)
      (($# >= 2)) || fail '--max-runners requires a value'
      max_runners="$2"
      shift 2
      ;;
    --port)
      (($# >= 2)) || fail '--port requires a value'
      port="$2"
      shift 2
      ;;
    --isolation)
      (($# >= 2)) || fail '--isolation requires a value'
      isolation="$2"
      shift 2
      ;;
    --runnerhub-tag)
      (($# >= 2)) || fail '--runnerhub-tag requires a value'
      runnerhub_tag="$2"
      shift 2
      ;;
    --runner-image)
      (($# >= 2)) || fail '--runner-image requires a value'
      runner_image="$2"
      shift 2
      ;;
    --dind-image)
      (($# >= 2)) || fail '--dind-image requires a value'
      dind_image="$2"
      shift 2
      ;;
    --output)
      (($# >= 2)) || fail '--output requires a value'
      output="$2"
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

[[ -f "$template" ]] || fail "template not found: $template"
[[ -n "$root" ]] || fail '--root is required'
[[ -n "$github_client_id" ]] || fail '--github-client-id is required'
[[ -n "$github_installation_id" ]] || fail '--github-installation-id is required'
[[ -n "$github_url" ]] || fail '--github-url is required'

root="${root%/}"
github_url="${github_url%/}"

[[ "$root" == /mnt/*/runnerhub ]] || fail '--root must look like /mnt/POOL/runnerhub'
[[ "$github_client_id" =~ ^[A-Za-z0-9._-]+$ ]] || fail 'GitHub App client ID contains unsupported characters'
[[ "$github_installation_id" =~ ^[0-9]+$ ]] || fail 'GitHub App installation ID must be numeric'
[[ "$github_url" =~ ^https://github\.com/[^/[:space:]]+/[^/[:space:]]+$ ]] || fail '--github-url must be a GitHub repository URL'
[[ "$max_runners" =~ ^[1-9][0-9]*$ ]] || fail '--max-runners must be a positive integer'
[[ "$port" =~ ^[0-9]+$ ]] || fail '--port must be numeric'
((port >= 1 && port <= 65535)) || fail '--port must be between 1 and 65535'
[[ "$runnerhub_tag" =~ ^[A-Za-z0-9_.-]+$ ]] || fail '--runnerhub-tag contains unsupported characters'
[[ -n "$runner_image" ]] || fail '--runner-image cannot be empty'
[[ -n "$dind_image" ]] || fail '--dind-image cannot be empty'

case "$isolation" in
  container-trusted)
    allow_privileged="false"
    ;;
  container-dind)
    allow_privileged="true"
    ;;
  *)
    fail '--isolation must be container-trusted or container-dind'
    ;;
esac

for marker in '/mnt/POOL/runnerhub' 'Iv1.CHANGE_ME' 'CHANGE_ME_INSTALLATION_ID' 'https://github.com/OWNER/REPOSITORY'; do
  grep -Fq "$marker" "$template" || fail "template marker missing: $marker"
done

root_esc="$(escape_sed_replacement "$root")"
client_esc="$(escape_sed_replacement "$github_client_id")"
installation_esc="$(escape_sed_replacement "$github_installation_id")"
github_url_esc="$(escape_sed_replacement "$github_url")"
max_runners_esc="$(escape_sed_replacement "$max_runners")"
port_esc="$(escape_sed_replacement "$port")"
isolation_esc="$(escape_sed_replacement "$isolation")"
allow_privileged_esc="$(escape_sed_replacement "$allow_privileged")"
tag_esc="$(escape_sed_replacement "$runnerhub_tag")"
runner_image_esc="$(escape_sed_replacement "$runner_image")"
dind_image_esc="$(escape_sed_replacement "$dind_image")"

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

sed \
  -e "s|/mnt/POOL/runnerhub|$root_esc|g" \
  -e "s|Iv1.CHANGE_ME|$client_esc|g" \
  -e "s|CHANGE_ME_INSTALLATION_ID|$installation_esc|g" \
  -e "s|https://github.com/OWNER/REPOSITORY|$github_url_esc|g" \
  -e "s|max-runners: &max-runners \"2\"|max-runners: \&max-runners \"$max_runners_esc\"|" \
  -e "s|published: \"8787\"|published: \"$port_esc\"|" \
  -e "s|pool-isolation: &pool-isolation container-trusted|pool-isolation: \&pool-isolation $isolation_esc|" \
  -e "s|allow-privileged-workers: &allow-privileged-workers \"false\"|allow-privileged-workers: \&allow-privileged-workers \"$allow_privileged_esc\"|" \
  -e "s|runner-image: &runner-image ghcr.io/actions/actions-runner:latest|runner-image: \&runner-image $runner_image_esc|" \
  -e "s|dind-image: &dind-image docker:dind|dind-image: \&dind-image $dind_image_esc|" \
  -e "s|ghcr.io/example-user/runnerhub-control:latest|ghcr.io/example-user/runnerhub-control:$tag_esc|" \
  -e "s|ghcr.io/example-user/runnerhub-agent:latest|ghcr.io/example-user/runnerhub-agent:$tag_esc|" \
  -e "s|ghcr.io/example-user/runnerhub-bootstrap:latest|ghcr.io/example-user/runnerhub-bootstrap:$tag_esc|" \
  -e "s|ghcr.io/example-user/runnerhub-scaleset:latest|ghcr.io/example-user/runnerhub-scaleset:$tag_esc|" \
  "$template" > "$tmp"

if grep -Eq 'CHANGE_ME|/mnt/POOL/runnerhub|github\.com/OWNER/REPOSITORY' "$tmp"; then
  fail 'generated Compose still contains an installation placeholder'
fi

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  docker compose -f "$tmp" config >/dev/null || fail 'generated Compose failed docker compose validation'
fi

if [[ -n "$output" ]]; then
  output_dir="$(dirname -- "$output")"
  [[ -d "$output_dir" ]] || fail "output directory does not exist: $output_dir"
  cp "$tmp" "$output"
  chmod 0644 "$output"
  printf 'Prepared TrueNAS Compose: %s\n' "$output" >&2
else
  cat "$tmp"
fi
