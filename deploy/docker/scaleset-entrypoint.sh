#!/bin/sh
set -eu

. /usr/local/lib/runnerhub/secret-file.sh
runnerhub_load_secret RUNNERHUB_CONTROL_TOKEN RUNNERHUB_CONTROL_TOKEN_FILE

runtime_dir=/run/runnerhub
install -d -m 0700 -o runnerhub -g runnerhub "$runtime_dir"

if [ -n "${RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE:-}" ]; then
  source_key="$RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE"
  if [ ! -r "$source_key" ]; then
    echo "RunnerHub scale-set listener cannot read the configured GitHub App private key: $source_key" >&2
    exit 1
  fi

  runtime_key="$runtime_dir/github-app.pem"
  install -m 0600 -o runnerhub -g runnerhub "$source_key" "$runtime_key"
  export RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE="$runtime_key"
fi

exec setpriv \
  --reuid=runnerhub \
  --regid=runnerhub \
  --init-groups \
  /usr/local/bin/runnerhub-scaleset "$@"
