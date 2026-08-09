#!/bin/sh
set -eu
mkdir -p /run/runnerhub-seed
mount -o ro /dev/vdb /run/runnerhub-seed
[ "$(cat /run/runnerhub-seed/manifest)" = "RUNNERHUB_SEED_V1" ]
JIT="$(cat /run/runnerhub-seed/jitconfig)"
cd /home/runner/actions-runner
exec ./run.sh --jitconfig "$JIT"
