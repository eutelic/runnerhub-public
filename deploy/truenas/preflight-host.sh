#!/usr/bin/env bash
set -euo pipefail

root="${1:-${RUNNERHUB_TRUENAS_ROOT:-/mnt/tank/runnerhub}}"
docker_socket="${RUNNERHUB_DOCKER_SOCKET:-/var/run/docker.sock}"

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

ok() {
  printf '[ ok ] %s\n' "$*"
}

note() {
  printf '[note] %s\n' "$*"
}

for cmd in midclt python3 openssl stat; do
  command -v "$cmd" >/dev/null 2>&1 || fail "$cmd is required on the TrueNAS host"
done

[[ "$root" == /* ]] || fail "RunnerHub root must be an absolute path: $root"
if [[ "$root" != /mnt/* ]]; then
  note "RunnerHub root is outside /mnt; TrueNAS datasets normally mount beneath /mnt"
fi

version_raw="$(midclt call system.version_short 2>/dev/null)" || fail "cannot read TrueNAS version via midclt"
version="$(python3 - "$version_raw" <<'PY'
import json
import re
import sys

raw = sys.argv[1].strip()
try:
    value = json.loads(raw)
except json.JSONDecodeError:
    value = raw
if not isinstance(value, str):
    raise SystemExit(1)
match = re.search(r"(\d+)\.(\d+)", value)
if not match:
    raise SystemExit(1)
major, minor = map(int, match.groups())
if (major, minor) < (24, 10):
    raise SystemExit(2)
print(value)
PY
)" || {
  rc=$?
  if [ "$rc" -eq 2 ]; then
    fail "RunnerHub Custom App deployment requires TrueNAS SCALE 24.10 or newer"
  fi
  fail "could not parse TrueNAS version: $version_raw"
}
ok "TrueNAS version is supported: $version"

docker_status_json="$(midclt call docker.status 2>/dev/null)" || fail "cannot read TrueNAS Docker/Apps status"
docker_status="$(printf '%s' "$docker_status_json" | python3 -c 'import json,sys; value=json.load(sys.stdin); print(value.get("status") or "")' 2>/dev/null)" || fail "could not parse TrueNAS Docker/Apps status"
[ "$docker_status" = "RUNNING" ] || fail "TrueNAS Apps/Docker service is not RUNNING (status: ${docker_status:-unknown})"
ok "TrueNAS Apps/Docker service is RUNNING"

docker_config_json="$(midclt call docker.config 2>/dev/null)" || fail "cannot read TrueNAS Docker/Apps configuration"
apps_pool="$(printf '%s' "$docker_config_json" | python3 -c 'import json,sys; value=json.load(sys.stdin); print(value.get("pool") or "")' 2>/dev/null)" || fail "could not parse TrueNAS Docker/Apps configuration"
[ -n "$apps_pool" ] || fail "TrueNAS Apps storage pool is not configured"
ok "TrueNAS Apps storage pool is configured: $apps_pool"

[ -S "$docker_socket" ] || fail "Docker socket is unavailable: $docker_socket"
ok "Docker socket is available: $docker_socket"

for dir in data agent secrets; do
  path="$root/$dir"
  [ ! -L "$path" ] || fail "RunnerHub dataset path must not be a symbolic link: $path"
  [ -d "$path" ] || fail "missing RunnerHub dataset/directory: $path"
  [ -r "$path" ] || fail "RunnerHub path is not readable: $path"
  [ -w "$path" ] || fail "RunnerHub path is not writable: $path"
  [ -x "$path" ] || fail "RunnerHub path is not traversable: $path"
done
ok "RunnerHub data, agent, and secrets paths are accessible"

key="$root/secrets/github-app.pem"
[ ! -L "$key" ] || fail "GitHub App private key must not be a symbolic link: $key"
[ -f "$key" ] || fail "missing GitHub App private key: $key"
[ -r "$key" ] || fail "GitHub App private key is not readable: $key"

mode="$(stat -c '%a' "$key" 2>/dev/null)" || fail "cannot read permissions for $key"
case "$mode" in
  400|600) ;;
  *) fail "GitHub App private key should be owner-only mode 0600 (or 0400); found $mode" ;;
esac

openssl pkey -in "$key" -noout >/dev/null 2>&1 || fail "GitHub App private key is not a valid private key: $key"
ok "GitHub App private key is valid and owner-only"

if command -v docker >/dev/null 2>&1; then
  if DOCKER_HOST="unix://$docker_socket" docker version >/dev/null 2>&1; then
    ok "host Docker CLI can reach the daemon"
  else
    note "host Docker CLI is present but cannot reach the daemon; RunnerHub's agent image performs its own Docker health check at startup"
  fi
else
  note "host Docker CLI is not installed; this is not required because the RunnerHub agent image includes its Docker client"
fi

echo
ok "RunnerHub TrueNAS host preflight passed"
