#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
preflight="$script_dir/preflight-host.sh"

tmp="$(mktemp -d)"
socket_pid=""
cleanup() {
  if [ -n "$socket_pid" ]; then
    kill "$socket_pid" >/dev/null 2>&1 || true
    wait "$socket_pid" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

root="$tmp/runnerhub"
mkdir -p "$root/data" "$root/agent" "$root/secrets" "$tmp/bin"
openssl genrsa -out "$root/secrets/github-app.pem" 2048 >/dev/null 2>&1
chmod 0600 "$root/secrets/github-app.pem"

socket="$tmp/docker.sock"
python3 - "$socket" <<'PY' &
import socket
import sys
import time
path = sys.argv[1]
sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
sock.bind(path)
sock.listen(1)
try:
    while True:
        time.sleep(1)
finally:
    sock.close()
PY
socket_pid=$!

for _ in $(seq 1 50); do
  [ -S "$socket" ] && break
  sleep 0.05
done
[ -S "$socket" ] || { echo 'test Docker socket did not start' >&2; exit 1; }

cat > "$tmp/bin/midclt" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "${1:-}" = call ] || exit 9
method="${2:-}"
mode="${FAKE_TRUENAS_MODE:-ok}"
case "$method" in
  system.version_short)
    if [ "$mode" = oldversion ]; then
      echo '"24.04.2"'
    elif [ "$mode" = badversion ]; then
      echo '"unknown"'
    else
      echo '"25.10.1"'
    fi
    ;;
  docker.status)
    if [ "$mode" = stopped ]; then
      echo '{"status":"STOPPED","description":"stopped"}'
    else
      echo '{"status":"RUNNING","description":"running"}'
    fi
    ;;
  docker.config)
    if [ "$mode" = unconfigured ]; then
      echo '{"pool":null,"dataset":null}'
    else
      echo '{"pool":"tank","dataset":"tank/ix-apps"}'
    fi
    ;;
  *) exit 8 ;;
esac
EOF
chmod +x "$tmp/bin/midclt"

cat > "$tmp/bin/docker" <<'EOF'
#!/bin/sh
[ "${1:-}" = version ] || exit 1
exit 0
EOF
chmod +x "$tmp/bin/docker"

PATH="$tmp/bin:$PATH" bash -n "$preflight"
RUNNERHUB_DOCKER_SOCKET="$socket" PATH="$tmp/bin:$PATH" \
  bash "$preflight" "$root" > "$tmp/success.log"
grep -Fq '[ ok ] TrueNAS version is supported: 25.10.1' "$tmp/success.log"
grep -Fq '[ ok ] TrueNAS Apps/Docker service is RUNNING' "$tmp/success.log"
grep -Fq '[ ok ] TrueNAS Apps storage pool is configured: tank' "$tmp/success.log"
grep -Fq '[ ok ] GitHub App private key is valid and protected:' "$tmp/success.log"
grep -Fq '[ ok ] RunnerHub TrueNAS host preflight passed' "$tmp/success.log"

# Scale Secrets publishes secret files as 0444 inside its protected 0700
# secrets directory. RunnerHub may consume that file directly without copying.
managed_dir="$tmp/scale-secrets/secrets"
mkdir -p "$managed_dir"
chmod 0700 "$tmp/scale-secrets" "$managed_dir"
openssl genrsa -out "$managed_dir/runnerhub_github_app" 2048 >/dev/null 2>&1
chmod 0444 "$managed_dir/runnerhub_github_app"
RUNNERHUB_DOCKER_SOCKET="$socket" PATH="$tmp/bin:$PATH" \
  bash "$preflight" "$root" "$managed_dir/runnerhub_github_app" > "$tmp/managed.log"
grep -Fq '[ ok ] GitHub App private key is valid and protected:' "$tmp/managed.log"

chmod 0755 "$managed_dir"
if RUNNERHUB_DOCKER_SOCKET="$socket" PATH="$tmp/bin:$PATH" \
  bash "$preflight" "$root" "$managed_dir/runnerhub_github_app" >/dev/null 2>&1; then
  echo 'host preflight unexpectedly accepted a mode-0444 key in a broad directory' >&2
  exit 1
fi
chmod 0700 "$managed_dir"

for mode in oldversion badversion stopped unconfigured; do
  if FAKE_TRUENAS_MODE="$mode" RUNNERHUB_DOCKER_SOCKET="$socket" PATH="$tmp/bin:$PATH" \
    bash "$preflight" "$root" >/dev/null 2>&1; then
    echo "host preflight unexpectedly accepted mode: $mode" >&2
    exit 1
  fi
done

if RUNNERHUB_DOCKER_SOCKET="$tmp/missing.sock" PATH="$tmp/bin:$PATH" \
  bash "$preflight" "$root" >/dev/null 2>&1; then
  echo 'host preflight unexpectedly accepted a missing Docker socket' >&2
  exit 1
fi

chmod 0644 "$root/secrets/github-app.pem"
if RUNNERHUB_DOCKER_SOCKET="$socket" PATH="$tmp/bin:$PATH" \
  bash "$preflight" "$root" >/dev/null 2>&1; then
  echo 'host preflight unexpectedly accepted an overly broad GitHub App key mode' >&2
  exit 1
fi
chmod 0600 "$root/secrets/github-app.pem"

mv "$root/secrets/github-app.pem" "$root/secrets/github-app.pem.real"
printf 'not a key\n' > "$root/secrets/github-app.pem"
chmod 0600 "$root/secrets/github-app.pem"
if RUNNERHUB_DOCKER_SOCKET="$socket" PATH="$tmp/bin:$PATH" \
  bash "$preflight" "$root" >/dev/null 2>&1; then
  echo 'host preflight unexpectedly accepted an invalid GitHub App private key' >&2
  exit 1
fi
rm "$root/secrets/github-app.pem"
mv "$root/secrets/github-app.pem.real" "$root/secrets/github-app.pem"
chmod 0600 "$root/secrets/github-app.pem"

mv "$root/agent" "$root/agent.real"
ln -s "$root/agent.real" "$root/agent"
if RUNNERHUB_DOCKER_SOCKET="$socket" PATH="$tmp/bin:$PATH" \
  bash "$preflight" "$root" >/dev/null 2>&1; then
  echo 'host preflight unexpectedly accepted a symlinked agent path' >&2
  exit 1
fi

printf 'TrueNAS host preflight tests passed.\n'
