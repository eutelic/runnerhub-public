#!/usr/bin/env bash
set -euo pipefail

base="${1:-}"
head="${2:-HEAD}"
out="${GITHUB_OUTPUT:-/dev/stdout}"

all_true() {
  for key in core minimum root_permission core_race scaleset agent docker_stack; do
    printf '%s=true\n' "$key" >> "$out"
  done
}

if [ -z "$base" ] || [ "$base" = "0000000000000000000000000000000000000000" ]; then
  all_true
  exit 0
fi

if ! git cat-file -e "$base^{commit}" 2>/dev/null || ! git cat-file -e "$head^{commit}" 2>/dev/null; then
  all_true
  exit 0
fi

mapfile -t changed < <(git diff --name-only "$base" "$head")
if [ "${#changed[@]}" -eq 0 ]; then
  all_true
  exit 0
fi

core=false
minimum=false
root_permission=false
core_race=false
scaleset=false
agent=false
docker_stack=false
broad=false

for path in "${changed[@]}"; do
  case "$path" in
    .github/workflows/ci.yml|scripts/ci-changes.sh|scripts/test-workflow-concurrency.sh)
      broad=true
      ;;
    go.mod|go.sum)
      core=true; minimum=true; root_permission=true; core_race=true; agent=true; docker_stack=true
      ;;
    web/*|cmd/runnerhubd/*|migrations/*)
      core=true
      ;;
    cmd/runnerhub-agent/*|internal/agent/*|internal/agenthub/*|internal/providers/*|internal/provider/*)
      core=true; minimum=true; root_permission=true; core_race=true; agent=true; docker_stack=true
      ;;
    cmd/runnerhub-deployer/*|internal/deployer/*)
      core=true; minimum=true; core_race=true
      ;;
    scaleset/*)
      scaleset=true; minimum=true
      ;;
    internal/*|cmd/*|tests/*)
      core=true; minimum=true; core_race=true; agent=true; docker_stack=true
      ;;
    deploy/docker/*|.dockerignore)
      docker_stack=true
      ;;
    deploy/truenas/*|scripts/init-docker-env.sh|scripts/start-docker.sh)
      core=true; docker_stack=true
      ;;
    .github/actionlint.yaml|.github/dependabot.yml|.github/workflows/*)
      core=true
      ;;
    *)
      # Unknown code-relevant input: fail conservative.
      broad=true
      ;;
  esac
done

if [ "$broad" = true ]; then
  all_true
  exit 0
fi

printf 'core=%s\n' "$core" >> "$out"
printf 'minimum=%s\n' "$minimum" >> "$out"
printf 'root_permission=%s\n' "$root_permission" >> "$out"
printf 'core_race=%s\n' "$core_race" >> "$out"
printf 'scaleset=%s\n' "$scaleset" >> "$out"
printf 'agent=%s\n' "$agent" >> "$out"
printf 'docker_stack=%s\n' "$docker_stack" >> "$out"
