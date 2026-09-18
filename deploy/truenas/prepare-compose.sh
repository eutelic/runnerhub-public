#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash deploy/truenas/prepare-compose.sh \
    --root /mnt/POOL/runnerhub \
    --github-app-id 123456789 \
    --github-client-id Iv1.EXAMPLE \
    --github-app-slug runnerhub-self-hosted-ci \
    --public-url http://TRUENAS_HOST:8787 \
    [--github-app-key-path /mnt/POOL/runnerhub/secrets/github-app.pem] \
    [--github-client-secret-path /mnt/POOL/runnerhub/secrets/github-client-secret] \
    [--port 8787] \
    [--isolation container-trusted|container-dind] \
    [--allow-privileged-workers true|false] \
    [--runnerhub-tag latest|sha-...] \
    [--runner-image IMAGE] \
    [--dind-image IMAGE] \
    [--output FILE]

The GitHub App key and OAuth client secret are referenced as file-backed
Compose secrets. Repository access is selected later in the RunnerHub web UI.
EOF
}

fail() {
  printf 'prepare-compose: %s\n' "$*" >&2
  exit 1
}

escape_sed_replacement() {
  printf '%s' "$1" | sed 's/[\&|]/\\&/g'
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
template="$script_dir/compose.yaml"

root=""
github_app_id=""
github_client_id=""
github_app_slug=""
public_url=""
github_app_key_path=""
github_client_secret_path=""
port="8787"
isolation="container-trusted"
runnerhub_tag="latest"
runner_image="ghcr.io/actions/actions-runner:2.337.0@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4"
dind_image="docker:dind"
allow_privileged_override=""
output=""

while (($#)); do
  case "$1" in
    --root) (($# >= 2)) || fail '--root requires a value'; root="$2"; shift 2 ;;
    --github-app-id) (($# >= 2)) || fail '--github-app-id requires a value'; github_app_id="$2"; shift 2 ;;
    --github-client-id) (($# >= 2)) || fail '--github-client-id requires a value'; github_client_id="$2"; shift 2 ;;
    --github-app-slug) (($# >= 2)) || fail '--github-app-slug requires a value'; github_app_slug="$2"; shift 2 ;;
    --public-url) (($# >= 2)) || fail '--public-url requires a value'; public_url="$2"; shift 2 ;;
    --github-app-key-path) (($# >= 2)) || fail '--github-app-key-path requires a value'; github_app_key_path="$2"; shift 2 ;;
    --github-client-secret-path) (($# >= 2)) || fail '--github-client-secret-path requires a value'; github_client_secret_path="$2"; shift 2 ;;
    --port) (($# >= 2)) || fail '--port requires a value'; port="$2"; shift 2 ;;
    --isolation) (($# >= 2)) || fail '--isolation requires a value'; isolation="$2"; shift 2 ;;
    --allow-privileged-workers) (($# >= 2)) || fail '--allow-privileged-workers requires a value'; allow_privileged_override="$2"; shift 2 ;;
    --runnerhub-tag) (($# >= 2)) || fail '--runnerhub-tag requires a value'; runnerhub_tag="$2"; shift 2 ;;
    --runner-image) (($# >= 2)) || fail '--runner-image requires a value'; runner_image="$2"; shift 2 ;;
    --dind-image) (($# >= 2)) || fail '--dind-image requires a value'; dind_image="$2"; shift 2 ;;
    --output) (($# >= 2)) || fail '--output requires a value'; output="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

[[ -f "$template" ]] || fail "template not found: $template"
[[ -n "$root" ]] || fail '--root is required'
[[ -n "$github_app_id" ]] || fail '--github-app-id is required'
[[ -n "$github_client_id" ]] || fail '--github-client-id is required'
[[ -n "$github_app_slug" ]] || fail '--github-app-slug is required'
[[ -n "$public_url" ]] || fail '--public-url is required'

root="${root%/}"
public_url="${public_url%/}"
if [[ -z "$github_app_key_path" ]]; then
  github_app_key_path="$root/secrets/github-app.pem"
fi
if [[ -z "$github_client_secret_path" ]]; then
  github_client_secret_path="$root/secrets/github-client-secret"
fi

[[ "$root" =~ ^/mnt/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*/runnerhub$ ]] || fail '--root must be an absolute TrueNAS dataset path ending in /runnerhub'
[[ "$github_app_key_path" =~ ^/mnt/[A-Za-z0-9._/-]+$ ]] || fail '--github-app-key-path must be an absolute TrueNAS /mnt path without whitespace'
[[ "$github_client_secret_path" =~ ^/mnt/[A-Za-z0-9._/-]+$ ]] || fail '--github-client-secret-path must be an absolute TrueNAS /mnt path without whitespace'
[[ "$github_app_key_path" != */ && "$github_client_secret_path" != */ ]] || fail 'GitHub secret paths must name files'
[[ "$github_app_id" =~ ^[1-9][0-9]*$ ]] || fail 'GitHub App ID must be a positive integer'
[[ "$github_client_id" =~ ^[A-Za-z0-9._-]+$ ]] || fail 'GitHub App client ID contains unsupported characters'
[[ "$github_app_slug" =~ ^[A-Za-z0-9-]+$ ]] || fail 'GitHub App slug contains unsupported characters'
case "$public_url" in
  http://*|https://*) ;;
  *) fail '--public-url must be an http(s) URL' ;;
esac
[[ "$public_url" != *[[:space:]]* ]] || fail '--public-url must not contain whitespace'
[[ "$port" =~ ^[0-9]+$ ]] || fail '--port must be numeric'
((port >= 1 && port <= 65535)) || fail '--port must be between 1 and 65535'
[[ "$runnerhub_tag" =~ ^[A-Za-z0-9_.-]+$ ]] || fail '--runnerhub-tag contains unsupported characters'
[[ "$runner_image" =~ ^[A-Za-z0-9./:_@-]+$ ]] || fail '--runner-image contains unsupported characters'
[[ "$dind_image" =~ ^[A-Za-z0-9./:_@-]+$ ]] || fail '--dind-image contains unsupported characters'

case "$isolation" in
  container-trusted) allow_privileged="false" ;;
  container-dind) allow_privileged="true" ;;
  *) fail '--isolation must be container-trusted or container-dind' ;;
esac
if [[ -n "$allow_privileged_override" ]]; then
  case "$allow_privileged_override" in
    true|false) allow_privileged="$allow_privileged_override" ;;
    *) fail '--allow-privileged-workers must be true or false' ;;
  esac
fi
if [[ "$isolation" == "container-dind" && "$allow_privileged" != "true" ]]; then
  fail '--isolation container-dind requires --allow-privileged-workers true'
fi

for marker in \
  '/mnt/POOL/runnerhub' \
  '/mnt/POOL/runnerhub/secrets/github-app.pem' \
  '/mnt/POOL/runnerhub/secrets/github-client-secret' \
  'github-app-id: &github-app-id 123456789' \
  'Iv1.CHANGE_ME' \
  'CHANGE_ME_APP_SLUG' \
  'http://TRUENAS_HOST:8787'
do
  grep -Fq "$marker" "$template" || fail "template marker missing: $marker"
done

root_esc="$(escape_sed_replacement "$root")"
key_path_esc="$(escape_sed_replacement "$github_app_key_path")"
client_secret_path_esc="$(escape_sed_replacement "$github_client_secret_path")"
app_id_esc="$(escape_sed_replacement "$github_app_id")"
client_esc="$(escape_sed_replacement "$github_client_id")"
slug_esc="$(escape_sed_replacement "$github_app_slug")"
public_url_esc="$(escape_sed_replacement "$public_url")"
port_esc="$(escape_sed_replacement "$port")"
isolation_esc="$(escape_sed_replacement "$isolation")"
allow_privileged_esc="$(escape_sed_replacement "$allow_privileged")"
tag_esc="$(escape_sed_replacement "$runnerhub_tag")"
runner_image_esc="$(escape_sed_replacement "$runner_image")"
dind_image_esc="$(escape_sed_replacement "$dind_image")"

work="$(mktemp -d)"
tmp="$work/compose.yaml"
trap 'rm -rf "$work"' EXIT

sed \
  -e "s|github-app-key-path: &github-app-key-path /mnt/POOL/runnerhub/secrets/github-app.pem|github-app-key-path: \&github-app-key-path $key_path_esc|" \
  -e "s|github-client-secret-path: &github-client-secret-path /mnt/POOL/runnerhub/secrets/github-client-secret|github-client-secret-path: \&github-client-secret-path $client_secret_path_esc|" \
  -e "s|/mnt/POOL/runnerhub|$root_esc|g" \
  -e "s|github-app-id: &github-app-id 123456789|github-app-id: \&github-app-id $app_id_esc|" \
  -e "s|Iv1.CHANGE_ME|$client_esc|g" \
  -e "s|CHANGE_ME_APP_SLUG|$slug_esc|g" \
  -e "s|http://TRUENAS_HOST:8787|$public_url_esc|g" \
  -e "s|published: \"8787\"|published: \"$port_esc\"|" \
  -e "s|pool-isolation: &pool-isolation container-trusted|pool-isolation: \&pool-isolation $isolation_esc|" \
  -e "s|allow-privileged-workers: &allow-privileged-workers \"false\"|allow-privileged-workers: \&allow-privileged-workers \"$allow_privileged_esc\"|" \
  -e "s|runner-image: &runner-image ghcr.io/actions/actions-runner:2.337.0@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4|runner-image: \&runner-image $runner_image_esc|" \
  -e "s|dind-image: &dind-image docker:dind|dind-image: \&dind-image $dind_image_esc|" \
  -e "s|ghcr.io/eutelic/runnerhub-control:latest|ghcr.io/eutelic/runnerhub-control:$tag_esc|" \
  -e "s|ghcr.io/eutelic/runnerhub-agent:latest|ghcr.io/eutelic/runnerhub-agent:$tag_esc|" \
  -e "s|ghcr.io/eutelic/runnerhub-bootstrap:latest|ghcr.io/eutelic/runnerhub-bootstrap:$tag_esc|" \
  -e "s|ghcr.io/eutelic/runnerhub-scaleset:latest|ghcr.io/eutelic/runnerhub-scaleset:$tag_esc|" \
  "$template" > "$tmp"

if grep -Eq 'CHANGE_ME|TRUENAS_HOST|/mnt/POOL/runnerhub' "$tmp"; then
  fail 'generated Compose still contains an installation placeholder'
fi

grep -Fq "data-path: &data-path $root/data" "$tmp" || fail 'data path rewrite did not apply'
grep -Fq "state-path: &state-path $root/agent" "$tmp" || fail 'state path rewrite did not apply'
grep -Fq "secrets-path: &secrets-path $root/secrets" "$tmp" || fail 'secrets path rewrite did not apply'
grep -Fq "github-app-key-path: &github-app-key-path $github_app_key_path" "$tmp" || fail 'GitHub App key path rewrite did not apply'
grep -Fq "github-client-secret-path: &github-client-secret-path $github_client_secret_path" "$tmp" || fail 'GitHub client secret path rewrite did not apply'
grep -Fq "github-app-id: &github-app-id $github_app_id" "$tmp" || fail 'GitHub App ID rewrite did not apply'
grep -Fq "github-client-id: &github-client-id $github_client_id" "$tmp" || fail 'GitHub client ID rewrite did not apply'
grep -Fq "github-app-slug: &github-app-slug $github_app_slug" "$tmp" || fail 'GitHub App slug rewrite did not apply'
grep -Fq "public-url: &public-url $public_url" "$tmp" || fail 'public URL rewrite did not apply'
grep -Fq "published: \"$port\"" "$tmp" || fail 'dashboard port rewrite did not apply'
grep -Fq "pool-isolation: &pool-isolation $isolation" "$tmp" || fail 'pool isolation rewrite did not apply'
grep -Fq "allow-privileged-workers: &allow-privileged-workers \"$allow_privileged\"" "$tmp" || fail 'privileged-worker rewrite did not apply'
grep -Fq "runner-image: &runner-image $runner_image" "$tmp" || fail 'runner image rewrite did not apply'
grep -Fq "dind-image: &dind-image $dind_image" "$tmp" || fail 'DinD image rewrite did not apply'
grep -Fq 'RUNNERHUB_DYNAMIC_CONFIG: "true"' "$tmp" || fail 'dynamic scale-set mode is missing'
for image in control agent bootstrap scaleset; do
  grep -Fq "ghcr.io/eutelic/runnerhub-${image}:$runnerhub_tag" "$tmp" || fail "RunnerHub $image image tag rewrite did not apply"
done

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  validation_key="$work/compose-validation-key"
  validation_client_secret="$work/compose-validation-client-secret"
  validation_compose="$work/compose-validation.yaml"
  printf '%s\n' 'runnerhub-compose-validation-only' > "$validation_key"
  printf '%s\n' 'runnerhub-client-secret-validation-only' > "$validation_client_secret"
  validation_key_esc="$(escape_sed_replacement "$validation_key")"
  validation_client_secret_esc="$(escape_sed_replacement "$validation_client_secret")"
  sed \
    -e "s|^    file: \*github-app-key-path$|    file: \"$validation_key_esc\"|" \
    -e "s|^    file: \*github-client-secret-path$|    file: \"$validation_client_secret_esc\"|" \
    "$tmp" > "$validation_compose"
  docker compose -f "$validation_compose" config >/dev/null || fail 'generated Compose failed docker compose validation'
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
