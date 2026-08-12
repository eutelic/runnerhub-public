#!/bin/sh
set -eu

fail() {
  echo "RunnerHub secret init: $*" >&2
  exit 1
}

secrets_dir="${RUNNERHUB_SECRETS_DIR:-/secrets}"
github_key="${RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE:-$secrets_dir/github-app.pem}"

mkdir -p "$secrets_dir"
[ -d "$secrets_dir" ] || fail "$secrets_dir is not a directory"
[ -w "$secrets_dir" ] || fail "$secrets_dir is not writable"
[ -r "$github_key" ] || fail "GitHub App private key is missing or unreadable: $github_key"
openssl pkey -in "$github_key" -noout >/dev/null 2>&1 || fail "GitHub App private key is not a valid private key: $github_key"

umask 077

generate_secret() {
  name="$1"
  destination="$secrets_dir/$name"

  if [ -e "$destination" ]; then
    [ -s "$destination" ] || fail "existing secret file is empty: $destination"
    return 0
  fi

  temporary="$secrets_dir/.${name}.$$"
  trap 'rm -f "$temporary"' EXIT INT TERM
  openssl rand -hex 32 > "$temporary"
  chmod 0600 "$temporary"
  mv "$temporary" "$destination"
  trap - EXIT INT TERM
}

generate_secret enrollment-token
generate_secret agent-secret
generate_secret admin-key
generate_secret scaleset-key

echo "RunnerHub persistent secrets are ready in $secrets_dir."
