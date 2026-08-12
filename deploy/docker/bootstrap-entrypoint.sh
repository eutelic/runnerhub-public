#!/bin/sh
set -eu

. /usr/local/lib/runnerhub/secret-file.sh
runnerhub_load_secret RUNNERHUB_ADMIN_KEY RUNNERHUB_ADMIN_KEY_FILE

fail() {
  echo "RunnerHub bootstrap: $*" >&2
  exit 1
}

control_url="${RUNNERHUB_CONTROL_URL:-http://control:8080}"
admin_key="${RUNNERHUB_ADMIN_KEY:-}"
github_url="${RUNNERHUB_GITHUB_URL:-}"
pool_id="${RUNNERHUB_POOL_ID:-pool-docker}"
scale_name="${RUNNERHUB_SCALESET_NAME:-runnerhub}"
max_runners="${RUNNERHUB_MAX_RUNNERS:-2}"
runner_group_id="${RUNNERHUB_RUNNER_GROUP_ID:-1}"
runner_image="${RUNNERHUB_RUNNER_IMAGE:-ghcr.io/actions/actions-runner:latest}"
pool_cpu="${RUNNERHUB_POOL_CPU_MILLIS:-4000}"
pool_memory="${RUNNERHUB_POOL_MEMORY_BYTES:-8589934592}"
pool_disk="${RUNNERHUB_POOL_DISK_BYTES:-21474836480}"
timeout="${RUNNERHUB_BOOTSTRAP_TIMEOUT_SECONDS:-180}"
provider="${RUNNERHUB_BOOTSTRAP_PROVIDER:-docker}"

[ -n "$admin_key" ] || fail "RUNNERHUB_ADMIN_KEY or RUNNERHUB_ADMIN_KEY_FILE is required"
[ -n "$github_url" ] || fail "RUNNERHUB_GITHUB_URL is required"

case "$github_url" in
  http://*|https://*) ;;
  *) fail "RUNNERHUB_GITHUB_URL must be an http(s) repository URL" ;;
esac
case "$github_url" in
  *\?*|*\#*) fail "RUNNERHUB_GITHUB_URL must not contain a query or fragment" ;;
esac

github_url="${github_url%/}"
authority_and_path="${github_url#*://}"
path="${authority_and_path#*/}"
[ "$path" != "$authority_and_path" ] || fail "RUNNERHUB_GITHUB_URL must identify a repository"
owner="${path%%/*}"
repo="${path#*/}"
[ -n "$owner" ] && [ -n "$repo" ] && [ "${repo#*/}" = "$repo" ] || fail "RUNNERHUB_GITHUB_URL must have exactly OWNER/REPOSITORY after the host"

printf '%s' "$owner" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9-]*$' || fail "repository owner contains unsupported characters"
printf '%s' "$repo" | grep -Eq '^[A-Za-z0-9._-]+$' || fail "repository name contains unsupported characters"
printf '%s' "$pool_id" | grep -Eq '^[A-Za-z0-9._-]+$' || fail "RUNNERHUB_POOL_ID contains unsupported characters"
printf '%s' "$scale_name" | grep -Eq '^[A-Za-z0-9._-]+$' || fail "RUNNERHUB_SCALESET_NAME contains unsupported characters"

for value_name in max_runners runner_group_id pool_cpu pool_memory pool_disk timeout; do
  eval "value=\${$value_name}"
  printf '%s' "$value" | grep -Eq '^[1-9][0-9]*$' || fail "$value_name must be a positive integer"
done

counter=0
while ! curl --fail --silent "$control_url/healthz" >/dev/null 2>&1; do
  counter=$((counter + 1))
  [ "$counter" -lt "$timeout" ] || fail "control plane did not become healthy within ${timeout}s"
  sleep 1
done

counter=0
while :; do
  hosts="$(curl --fail --silent -H "Authorization: Bearer ${admin_key}" "$control_url/api/v1/hosts" 2>/dev/null || true)"
  if printf '%s' "$hosts" | jq -e --arg provider "$provider" 'any(.[]; .provider == $provider and .state == "online")' >/dev/null 2>&1; then
    break
  fi
  counter=$((counter + 1))
  [ "$counter" -lt "$timeout" ] || fail "$provider agent did not enroll and become online within ${timeout}s"
  sleep 1
done

payload="$(jq -n \
  --arg id "$pool_id" \
  --arg owner "$owner" \
  --arg repository "$repo" \
  --arg label "$scale_name" \
  --arg image "$runner_image" \
  --argjson maxConcurrency "$max_runners" \
  --argjson runnerGroupId "$runner_group_id" \
  --argjson cpuMillis "$pool_cpu" \
  --argjson memoryBytes "$pool_memory" \
  --argjson diskBytes "$pool_disk" \
  '{
    id: $id,
    name: "runnerhub",
    scope: {kind: "repository", owner: $owner, repository: $repository},
    labels: [$label],
    isolation: "container-trusted",
    resources: {cpuMillis: $cpuMillis, memoryBytes: $memoryBytes, diskBytes: $diskBytes},
    minIdle: 0,
    maxConcurrency: $maxConcurrency,
    runnerGroupId: $runnerGroupId,
    image: $image
  }')"

echo "Configuring RunnerHub pool $pool_id for $owner/$repo..."
curl --fail-with-body --silent --show-error \
  -X POST \
  -H "Authorization: Bearer ${admin_key}" \
  -H 'Content-Type: application/json' \
  "$control_url/api/v1/pools" \
  -d "$payload" >/dev/null

echo "RunnerHub pool $pool_id is configured."
