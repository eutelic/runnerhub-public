#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
envfile="${1:-$root/deploy/docker/.env}"
compose_file="$root/deploy/docker/compose.yaml"

fail() {
  echo "RunnerHub bootstrap: $*" >&2
  exit 1
}

for cmd in docker curl; do
  command -v "$cmd" >/dev/null 2>&1 || fail "$cmd is required"
done

docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is required"
[ -f "$envfile" ] || fail "$envfile does not exist; run scripts/init-docker-env.sh first"

set -a
# The generated file is intentionally shell-compatible as well as Compose-compatible.
# shellcheck disable=SC1090
source "$envfile"
set +a

required=(
  RUNNERHUB_ENROLLMENT_TOKEN
  RUNNERHUB_AGENT_SECRET
  RUNNERHUB_ADMIN_KEY
  RUNNERHUB_API_KEYS
  RUNNERHUB_GITHUB_APP_ISSUER
  RUNNERHUB_GITHUB_APP_INSTALLATION_ID
  RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE
  RUNNERHUB_GITHUB_URL
)
for name in "${required[@]}"; do
  [ -n "${!name:-}" ] || fail "$name is required in $envfile"
done

case "$RUNNERHUB_GITHUB_APP_ISSUER" in
  REPLACE_*|Iv1.replace-*) fail "set RUNNERHUB_GITHUB_APP_ISSUER in $envfile" ;;
esac
[[ "$RUNNERHUB_GITHUB_APP_INSTALLATION_ID" =~ ^[0-9]+$ ]] || fail "RUNNERHUB_GITHUB_APP_INSTALLATION_ID must be numeric"
[ -r "$RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE" ] || fail "GitHub App private key is not readable: $RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE"

case "$RUNNERHUB_GITHUB_URL" in
  http://*|https://*) ;;
  *) fail "RUNNERHUB_GITHUB_URL must be an http(s) repository URL" ;;
esac
case "$RUNNERHUB_GITHUB_URL" in
  *\?*|*\#*) fail "RUNNERHUB_GITHUB_URL must not contain a query or fragment" ;;
esac

github_url="${RUNNERHUB_GITHUB_URL%/}"
authority_and_path="${github_url#*://}"
path="${authority_and_path#*/}"
[ "$path" != "$authority_and_path" ] || fail "RUNNERHUB_GITHUB_URL must identify a repository"
owner="${path%%/*}"
repo="${path#*/}"
[ -n "$owner" ] && [ -n "$repo" ] && [[ "$repo" != */* ]] || fail "RUNNERHUB_GITHUB_URL must have exactly OWNER/REPOSITORY after the host"

pool_id="${RUNNERHUB_POOL_ID:-pool-docker}"
scale_name="${RUNNERHUB_SCALESET_NAME:-runnerhub}"
max_runners="${RUNNERHUB_MAX_RUNNERS:-2}"
runner_group_id="${RUNNERHUB_RUNNER_GROUP_ID:-1}"
runner_image="${RUNNERHUB_RUNNER_IMAGE:-ghcr.io/actions/actions-runner:latest}"
[[ "$max_runners" =~ ^[1-9][0-9]*$ ]] || fail "RUNNERHUB_MAX_RUNNERS must be a positive integer"
[[ "$runner_group_id" =~ ^[1-9][0-9]*$ ]] || fail "RUNNERHUB_RUNNER_GROUP_ID must be a positive integer"

compose=(docker compose --env-file "$envfile" -f "$compose_file")

echo "Starting RunnerHub control plane and Docker host agent..."
"${compose[@]}" up -d --build control agent

healthy=false
for _ in $(seq 1 60); do
  if curl --fail --silent http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
    healthy=true
    break
  fi
  sleep 1
done
if [ "$healthy" != true ]; then
  "${compose[@]}" logs --tail=100 control >&2 || true
  fail "control plane did not become healthy"
fi

enrolled=false
for _ in $(seq 1 60); do
  hosts="$(curl --fail --silent -H "Authorization: Bearer ${RUNNERHUB_ADMIN_KEY}" http://127.0.0.1:8080/api/v1/hosts 2>/dev/null || true)"
  if printf '%s' "$hosts" | grep -q '"provider":"docker"' && printf '%s' "$hosts" | grep -q '"state":"online"'; then
    enrolled=true
    break
  fi
  sleep 1
done
if [ "$enrolled" != true ]; then
  "${compose[@]}" logs --tail=100 control agent >&2 || true
  fail "Docker agent did not enroll and become online"
fi

pool_payload=$(cat <<EOF
{
  "id": "$pool_id",
  "name": "runnerhub",
  "scope": {
    "kind": "repository",
    "owner": "$owner",
    "repository": "$repo"
  },
  "labels": ["$scale_name"],
  "isolation": "container-trusted",
  "resources": {
    "cpuMillis": 4000,
    "memoryBytes": 8589934592,
    "diskBytes": 21474836480
  },
  "minIdle": 0,
  "maxConcurrency": $max_runners,
  "runnerGroupId": $runner_group_id,
  "image": "$runner_image"
}
EOF
)

echo "Configuring repository pool $pool_id for $owner/$repo..."
curl --fail-with-body --silent --show-error \
  -X POST \
  -H "Authorization: Bearer ${RUNNERHUB_ADMIN_KEY}" \
  -H 'Content-Type: application/json' \
  http://127.0.0.1:8080/api/v1/pools \
  -d "$pool_payload" >/dev/null

echo "Starting outbound GitHub scale-set listener..."
"${compose[@]}" --profile autoscale up -d --build scaleset

cat <<EOF
RunnerHub automatic Docker setup is running.

Repository: $owner/$repo
Pool:       $pool_id
Runs-on:    $scale_name

Use this in the repository workflow:

  runs-on: $scale_name

Inspect the listener with:
  docker compose --env-file "$envfile" -f "$compose_file" --profile autoscale logs -f scaleset
EOF
