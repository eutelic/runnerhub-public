#!/bin/sh
set -eu

runtime_dir=/run/runnerhub
install -d -m 0700 -o runnerhub -g runnerhub "$runtime_dir"

if [ -n "${RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE:-}" ]; then
  source_key="$RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE"
  if [ ! -r "$source_key" ]; then
    echo "RunnerHub cannot read the configured GitHub App private key: $source_key" >&2
    exit 1
  fi

  runtime_key="$runtime_dir/github-app.pem"
  install -m 0600 -o runnerhub -g runnerhub "$source_key" "$runtime_key"
  export RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE="$runtime_key"
fi

# A newly-created named volume may not retain the ownership baked into the
# image. Repair only RunnerHub's dedicated data directory before dropping
# privileges; the daemon itself never runs as root.
chown runnerhub:runnerhub /data

exec setpriv \
  --reuid=runnerhub \
  --regid=runnerhub \
  --init-groups \
  /usr/local/bin/runnerhubd "$@"
