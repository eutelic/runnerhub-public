#!/usr/bin/env bash
set -euo pipefail

image="${1:-runnerhub-ci-control}"
host_uid="$(id -u)"
host_gid="$(id -g)"

tmp="$(mktemp -d)"

run_chown() {
  local owner="$1"
  local path="$2"
  if [ "$host_uid" -eq 0 ]; then
    chown "$owner" "$path"
  elif command -v sudo >/dev/null 2>&1; then
    sudo chown "$owner" "$path"
  else
    echo 'test-control-data-ownership: root or sudo is required for ownership migration checks' >&2
    exit 1
  fi
}

cleanup() {
  if [ "$host_uid" -eq 0 ]; then
    rm -rf "$tmp"
  elif command -v sudo >/dev/null 2>&1; then
    sudo rm -rf "$tmp"
  else
    rm -rf "$tmp" || true
  fi
}
trap cleanup EXIT

mkdir -p "$tmp/data"
touch "$tmp/data/runnerhub.db"
chmod 0600 "$tmp/data/runnerhub.db"

stat_uid() { stat -c '%u' "$1"; }
stat_gid() { stat -c '%g' "$1"; }

run_chown 12345:12345 "$tmp/data/runnerhub.db"
[ "$(stat_uid "$tmp/data/runnerhub.db")" = "12345" ]

common_env=(
  -e RUNNERHUB_ENROLLMENT_TOKEN=test-enrollment
  -e RUNNERHUB_AGENT_SECRET=test-agent-secret-012345678901234567890123456789
  -e RUNNERHUB_ADMIN_KEY=test-admin
  -e RUNNERHUB_SCALESET_KEY=test-scaler
  -e RUNNERHUB_DB=/data/runnerhub.db
)

# Replace runnerhubd inside the disposable container with a probe. The production
# entrypoint still performs secret loading, ownership repair, and setpriv exactly
# as it does in the shipped image.
docker run --rm \
  -v "$tmp/data:/data" \
  "${common_env[@]}" \
  --entrypoint /bin/sh \
  "$image" \
  -c '
    set -eu
    cat > /usr/local/bin/runnerhubd <<"EOF"
#!/bin/sh
set -eu
printf "%s:%s\n" "$(id -u)" "$(id -g)" > /data/runtime-identity
EOF
    chmod 0755 /usr/local/bin/runnerhubd
    exec /usr/local/bin/runnerhub-control-entrypoint
  '

[ "$(stat_uid "$tmp/data/runnerhub.db")" = "10001" ] || {
  echo "persisted DB UID was not migrated to 10001" >&2
  exit 1
}
[ "$(stat_gid "$tmp/data/runnerhub.db")" = "10001" ] || {
  echo "persisted DB GID was not migrated to 10001" >&2
  exit 1
}
[ "$(cat "$tmp/data/runtime-identity")" = "10001:10001" ] || {
  echo "control process did not drop to the fixed RunnerHub identity" >&2
  exit 1
}

# The entrypoint correctly changed the bind-mounted directory owner too. Give the
# test process temporary ownership again so it can set up the symlink scenario.
run_chown "$host_uid:$host_gid" "$tmp/data"
run_chown "$host_uid:$host_gid" "$tmp/data/runnerhub.db"
run_chown "$host_uid:$host_gid" "$tmp/data/runtime-identity"

# The root entrypoint must not follow an attacker-/corruption-controlled symlink
# while repairing database ownership.
rm -f "$tmp/data/runnerhub.db" "$tmp/data/runtime-identity"
touch "$tmp/outside-db"
ln -s "$tmp/outside-db" "$tmp/data/runnerhub.db"

if docker run --rm \
  -v "$tmp/data:/data" \
  "${common_env[@]}" \
  --entrypoint /bin/sh \
  "$image" \
  -c '
    set -eu
    cat > /usr/local/bin/runnerhubd <<"EOF"
#!/bin/sh
exit 0
EOF
    chmod 0755 /usr/local/bin/runnerhubd
    exec /usr/local/bin/runnerhub-control-entrypoint
  ' >/dev/null 2>&1; then
  echo 'control entrypoint unexpectedly accepted a symlinked database' >&2
  exit 1
fi

[ "$(stat_uid "$tmp/outside-db")" = "$host_uid" ] || {
  echo 'control entrypoint followed the symlink and changed the target owner' >&2
  exit 1
}

printf 'Control persistent-data ownership tests passed.\n'
