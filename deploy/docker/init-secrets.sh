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

validate_secret() {
  path="$1"
  name="$2"

  [ ! -L "$path" ] || fail "$name must not be a symbolic link: $path"
  [ -f "$path" ] || fail "$name must be a regular file: $path"

  value="$(cat "$path")"
  [ "${#value}" -eq 64 ] || fail "$name must contain exactly 64 hexadecimal characters: $path"
  case "$value" in
    *[!0-9a-fA-F]*) fail "$name contains non-hexadecimal characters: $path" ;;
  esac

  chmod 0600 "$path"
}

ensure_secret() {
  name="$1"
  destination="$secrets_dir/$name"

  if [ -e "$destination" ] || [ -L "$destination" ]; then
    validate_secret "$destination" "$name"
    return 0
  fi

  temporary="$secrets_dir/.${name}.$$"
  trap 'rm -f "$temporary"' EXIT INT TERM
  openssl rand -hex 32 > "$temporary"
  chmod 0600 "$temporary"
  mv "$temporary" "$destination"
  trap - EXIT INT TERM

  validate_secret "$destination" "$name"
}

ensure_secret enrollment-token
ensure_secret agent-secret
ensure_secret admin-key
ensure_secret scaleset-key

echo "RunnerHub persistent secrets are ready in $secrets_dir."
