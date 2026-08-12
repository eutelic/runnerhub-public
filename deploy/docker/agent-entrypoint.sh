#!/bin/sh
set -eu

. /usr/local/lib/runnerhub/secret-file.sh
runnerhub_load_secret RUNNERHUB_ENROLLMENT_TOKEN RUNNERHUB_ENROLLMENT_TOKEN_FILE

exec /usr/local/bin/runnerhub-agent "$@"
