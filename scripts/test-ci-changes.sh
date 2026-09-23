#!/usr/bin/env bash
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

run_case() {
  local name="$1" changed="$2"
  shift 2
  local tmp base head out
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  git init -q "$tmp/repo"
  cd "$tmp/repo"
  git config user.email ci@example.invalid
  git config user.name ci
  mkdir -p scripts web internal/agent scaleset .github/workflows
  cp "$root/scripts/ci-changes.sh" scripts/ci-changes.sh
  printf 'base\n' > README.md
  git add .
  git commit -qm base
  base="$(git rev-parse HEAD)"
  eval "$changed"
  git add .
  git commit -qm change
  head="$(git rev-parse HEAD)"
  out="$tmp/out"
  GITHUB_OUTPUT="$out" bash scripts/ci-changes.sh "$base" "$head"
  for expected in "$@"; do
    grep -qx "$expected" "$out" || {
      echo "$name: missing $expected" >&2
      cat "$out" >&2
      exit 1
    }
  done
  cd "$root"
  rm -rf "$tmp"
  trap - RETURN
}

run_case ui_only "printf 'x\n' > web/app.js" \
  core=true minimum=false root_permission=false core_race=false scaleset=false agent=false docker_stack=false
run_case agent "printf 'x\n' > internal/agent/client.go" \
  core=true minimum=true root_permission=true core_race=true scaleset=false agent=true docker_stack=true
run_case scaleset "printf 'x\n' > scaleset/main.go" \
  core=false minimum=true root_permission=false core_race=false scaleset=true agent=false docker_stack=false
run_case workflow "printf 'x\n' > .github/workflows/ci.yml" \
  core=true minimum=true root_permission=true core_race=true scaleset=true agent=true docker_stack=true

echo "[ ok ] deterministic CI affected-job planner"
