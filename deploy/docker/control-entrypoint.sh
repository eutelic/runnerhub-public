#!/bin/sh
set -eu

. /usr/local/lib/runnerhub/secret-file.sh
runnerhub_load_secret RUNNERHUB_ENROLLMENT_TOKEN RUNNERHUB_ENROLLMENT_TOKEN_FILE
runnerhub_load_secret RUNNERHUB_AGENT_SECRET RUNNERHUB_AGENT_SECRET_FILE
runnerhub_load_secret RUNNERHUB_ADMIN_KEY RUNNERHUB_ADMIN_KEY_FILE
runnerhub_load_secret RUNNERHUB_SCALESET_KEY RUNNERHUB_SCALESET_KEY_FILE

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

# Release/TrueNAS Compose can provide the two credentials separately so the
# scaler key does not need to be repeated inside a hand-built API key string.
if [ -z "${RUNNERHUB_API_KEYS:-}" ] && [ -n "${RUNNERHUB_ADMIN_KEY:-}" ] && [ -n "${RUNNERHUB_SCALESET_KEY:-}" ]; then
  export RUNNERHUB_API_KEYS="${RUNNERHUB_ADMIN_KEY}:admin:local-admin,${RUNNERHUB_SCALESET_KEY}:scaler:scaleset-listener"
fi

# A newly-created named volume or dedicated bind mount may not retain the
# ownership baked into the image. The image uses a fixed runnerhub UID/GID, but
# repair an existing database once here as well so the first upgrade from older
# auto-assigned service UIDs and restores from backup remain readable.
chown runnerhub:runnerhub /data

db_path="${RUNNERHUB_DB:-/data/runnerhub.db}"
case "$db_path" in
  /data/*) ;;
  *)
    echo "RunnerHub container database path must remain beneath /data: $db_path" >&2
    exit 1
    ;;
esac

repair_db_file() {
  path="$1"
  if [ -L "$path" ]; then
    echo "RunnerHub database state must not be a symbolic link: $path" >&2
    exit 1
  fi
  if [ -e "$path" ]; then
    chown runnerhub:runnerhub "$path"
  fi
}

repair_db_file "$db_path"
repair_db_file "$db_path-wal"
repair_db_file "$db_path-shm"
repair_db_file "$db_path-journal"

exec setpriv \
  --reuid=runnerhub \
  --regid=runnerhub \
  --init-groups \
  /usr/local/bin/runnerhubd "$@"
