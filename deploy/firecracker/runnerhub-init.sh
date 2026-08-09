#!/bin/sh
set -eu

ip link set eth0 up
if command -v udhcpc >/dev/null 2>&1; then
  udhcpc -q -n -i eth0
elif command -v dhclient >/dev/null 2>&1; then
  dhclient -1 eth0
else
  echo 'runnerhub: guest image needs udhcpc or dhclient for eth0' >&2
  exit 1
fi

mkdir -p /run/runnerhub-seed
mount -o ro /dev/vdb /run/runnerhub-seed
[ "$(cat /run/runnerhub-seed/manifest)" = "RUNNERHUB_SEED_V1" ]
JIT="$(cat /run/runnerhub-seed/jitconfig)"
cd /home/runner/actions-runner
exec ./run.sh --jitconfig "$JIT"
