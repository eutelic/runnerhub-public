#!/usr/bin/env bash
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
app="$root/ix-dev/community/runnerhub"

for path in \
  "$app/app.yaml" \
  "$app/ix_values.yaml" \
  "$app/questions.yaml" \
  "$app/README.md" \
  "$app/templates/docker-compose.yaml" \
  "$app/templates/test_values/basic-values.yaml" \
  "$app/templates/test_values/github-host-path-values.yaml"; do
  test -s "$path" || {
    echo "missing catalog file: $path" >&2
    exit 1
  }
done

template="$app/templates/docker-compose.yaml"
values="$app/ix_values.yaml"

if grep -niE 'midclt|RUNNERHUB_PROVIDER[^[:space:]]*truenas' "$template"; then
  echo "catalog template must not use the TrueNAS middleware provider" >&2
  exit 1
fi

if grep -niE 'RUNNERHUB_ALLOW_PRIVILEGED_WORKERS.*(true|1)|set_privileged' "$template"; then
  echo "catalog template must not enable privileged workers" >&2
  exit 1
fi

if grep -niE 'tag:[[:space:]]*["'\'' ]*latest|runnerhub-(control|agent|bootstrap|scaleset):latest' "$values" "$template"; then
  echo "catalog package must not use moving RunnerHub latest tags" >&2
  exit 1
fi

grep -Fq 'RUNNERHUB_PROVIDER", "docker"' "$template"
grep -Fq 'agent.add_docker_socket()' "$template"
grep -Fq 'RUNNERHUB_ALLOW_PRIVILEGED_WORKERS", "false"' "$template"
grep -Fq 'private: true' "$app/questions.yaml"
grep -Fq 'host_path: /var/run/docker.sock' "$app/app.yaml"
grep -Fq 'lib_version: 2.3.18' "$app/app.yaml"

echo "[ ok ] TrueNAS catalog staging policy"
