#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

tmp="$(mktemp -d "${RUNNER_TEMP:-/tmp}/runnerhub-secret-init.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
secret_dir="$tmp/secrets"
mkdir -p "$secret_dir"
openssl genrsa -out "$secret_dir/github-app.pem" 2048 >/dev/null 2>&1
chmod 0600 "$secret_dir/github-app.pem"

run_init() {
  RUNNERHUB_SECRETS_DIR="$secret_dir" \
    RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE="$secret_dir/github-app.pem" \
    sh deploy/docker/init-secrets.sh
}

run_init

for secret in enrollment-token agent-secret admin-key scaleset-key; do
  path="$secret_dir/$secret"
  test -f "$path"
  test ! -L "$path"
  test "$(wc -c < "$path")" -eq 65
  value="$(cat "$path")"
  test "${#value}" -eq 64
  [[ "$value" =~ ^[0-9a-fA-F]{64}$ ]]
  test "$(stat -c '%a' "$path")" = "600"
done

before="$(sha256sum \
  "$secret_dir/enrollment-token" \
  "$secret_dir/agent-secret" \
  "$secret_dir/admin-key" \
  "$secret_dir/scaleset-key")"
run_init
after="$(sha256sum \
  "$secret_dir/enrollment-token" \
  "$secret_dir/agent-secret" \
  "$secret_dir/admin-key" \
  "$secret_dir/scaleset-key")"
test "$before" = "$after"

# RunnerHub can also initialize its internal credentials without GitHub enabled.
# This is used by catalog smoke tests and local control-plane evaluation.
no_github_dir="$tmp/no-github-secrets"
mkdir -p "$no_github_dir"
RUNNERHUB_SECRETS_DIR="$no_github_dir" \
  RUNNERHUB_REQUIRE_GITHUB_APP_KEY=false \
  sh deploy/docker/init-secrets.sh
test ! -e "$no_github_dir/github-app.pem"
for secret in enrollment-token agent-secret admin-key scaleset-key; do
  test -f "$no_github_dir/$secret"
done

# TrueNAS catalog installs collect the GitHub credentials as masked form values.
# Prove that the one-shot bootstrap can materialize those values into persistent
# files while preserving RunnerHub's generated internal credentials.
catalog_dir="$tmp/catalog-secrets"
catalog_key="$tmp/catalog-github-app.pem"
mkdir -p "$catalog_dir"
openssl genrsa -out "$catalog_key" 2048 >/dev/null 2>&1
catalog_client_secret='catalog-client-secret-value'

run_catalog_init() {
  RUNNERHUB_SECRETS_DIR="$catalog_dir" \
    RUNNERHUB_GITHUB_APP_PRIVATE_KEY="$(cat "$catalog_key")" \
    RUNNERHUB_GITHUB_CLIENT_SECRET="$catalog_client_secret" \
    sh deploy/docker/init-secrets.sh
}

run_catalog_init
test -f "$catalog_dir/github-app.pem"
test -f "$catalog_dir/github-client-secret"
test ! -L "$catalog_dir/github-app.pem"
test ! -L "$catalog_dir/github-client-secret"
openssl pkey -in "$catalog_dir/github-app.pem" -noout >/dev/null 2>&1
test "$(cat "$catalog_dir/github-app.pem")" = "$(cat "$catalog_key")"
test "$(cat "$catalog_dir/github-client-secret")" = "$catalog_client_secret"
test "$(stat -c '%a' "$catalog_dir/github-app.pem")" = "600"
test "$(stat -c '%a' "$catalog_dir/github-client-secret")" = "600"

catalog_internal_before="$(sha256sum \
  "$catalog_dir/enrollment-token" \
  "$catalog_dir/agent-secret" \
  "$catalog_dir/admin-key" \
  "$catalog_dir/scaleset-key")"
catalog_client_secret='rotated-catalog-client-secret'
run_catalog_init
catalog_internal_after="$(sha256sum \
  "$catalog_dir/enrollment-token" \
  "$catalog_dir/agent-secret" \
  "$catalog_dir/admin-key" \
  "$catalog_dir/scaleset-key")"
test "$catalog_internal_before" = "$catalog_internal_after"
test "$(cat "$catalog_dir/github-client-secret")" = "$catalog_client_secret"

chmod 0644 "$secret_dir/admin-key"
run_init
test "$(stat -c '%a' "$secret_dir/admin-key")" = "600"

cp "$secret_dir/admin-key" "$tmp/admin-key.good"
printf '   \n' > "$secret_dir/admin-key"
if run_init; then
  echo 'whitespace-only persisted secret unexpectedly succeeded' >&2
  exit 1
fi

cp "$tmp/admin-key.good" "$secret_dir/admin-key"
printf '%s\n' 'xyz-not-a-valid-secret' > "$secret_dir/admin-key"
if run_init; then
  echo 'malformed persisted secret unexpectedly succeeded' >&2
  exit 1
fi

rm -f "$secret_dir/admin-key"
ln -s "$tmp/admin-key.good" "$secret_dir/admin-key"
if run_init; then
  echo 'symlinked persisted secret unexpectedly succeeded' >&2
  exit 1
fi

echo "[ ok ] persistent secret lifecycle"
