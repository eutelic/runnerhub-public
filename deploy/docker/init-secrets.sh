#!/bin/sh
set -eu

fail() {
  echo "RunnerHub secret init: $*" >&2
  exit 1
}

secrets_dir="${RUNNERHUB_SECRETS_DIR:-/secrets}"
default_github_key="$secrets_dir/github-app.pem"
default_github_client_secret="$secrets_dir/github-client-secret"
require_github_key="${RUNNERHUB_REQUIRE_GITHUB_APP_KEY:-true}"

case "$require_github_key" in
  true|false) ;;
  *) fail "RUNNERHUB_REQUIRE_GITHUB_APP_KEY must be true or false" ;;
esac

mkdir -p "$secrets_dir"
[ -d "$secrets_dir" ] || fail "$secrets_dir is not a directory"
[ -w "$secrets_dir" ] || fail "$secrets_dir is not writable"

umask 077

materialize_input_secret() {
  destination="$1"
  value="$2"
  name="$3"

  [ -n "$value" ] || fail "$name must not be empty"
  if [ -e "$destination" ] || [ -L "$destination" ]; then
    [ ! -L "$destination" ] || fail "$name destination must not be a symbolic link: $destination"
    [ -f "$destination" ] || fail "$name destination must be a regular file: $destination"
  fi

  temporary="$secrets_dir/.$(basename "$destination").$$"
  trap 'rm -f "$temporary"' EXIT INT TERM
  printf '%s' "$value" > "$temporary"
  chmod 0600 "$temporary"
  mv -f "$temporary" "$destination"
  trap - EXIT INT TERM
}

# Catalog-style installers can only collect credentials as form values. Accept
# those values on the short-lived bootstrap container and immediately
# materialize them into the persistent secrets volume. Long-running services
# continue to consume files rather than carrying the PEM/client secret in their
# environment.
if [ -n "${RUNNERHUB_GITHUB_APP_PRIVATE_KEY:-}" ]; then
  materialize_input_secret     "$default_github_key"     "$RUNNERHUB_GITHUB_APP_PRIVATE_KEY"     "GitHub App private key"
fi
if [ -n "${RUNNERHUB_GITHUB_CLIENT_SECRET:-}" ]; then
  materialize_input_secret     "$default_github_client_secret"     "$RUNNERHUB_GITHUB_CLIENT_SECRET"     "GitHub client secret"
fi

github_key="${RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE:-$default_github_key}"
if [ "$require_github_key" = "true" ] || [ -e "$github_key" ] || [ -L "$github_key" ]; then
  [ -r "$github_key" ] || fail "GitHub App private key is missing or unreadable: $github_key"
  [ ! -L "$github_key" ] || fail "GitHub App private key must not be a symbolic link: $github_key"
  [ -f "$github_key" ] || fail "GitHub App private key must be a regular file: $github_key"
  openssl pkey -in "$github_key" -noout >/dev/null 2>&1 || fail "GitHub App private key is not a valid private key: $github_key"
fi

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
