#!/usr/bin/env bash
# Read-only host preflight. Never prints environment variables or credentials.
set -euo pipefail

usage() {
  printf 'Usage: bash scripts/recovery-preflight.sh [--output DIR]\n'
}
output=''
while (($#)); do
  case "$1" in
    --output) (($# >= 2)) || { usage >&2; exit 2; }; output="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

if [[ -n "$output" ]]; then
  if [[ -e "$output" ]]; then
    printf '[fail] output path already exists; refusing to overwrite: %s\n' "$output" >&2
    exit 2
  fi
  # Protect both the directory and the tee-created evidence file. A private
  # directory alone does not prevent accidental disclosure if the file moves.
  umask 077
  mkdir -m 700 -- "$output"
  exec > >(tee "$output/preflight.txt") 2>&1
fi

blockers=0
printf 'RunnerHub recovery preflight (read-only)\n'
printf 'UTC: %s\n' "$(date -u +%FT%TZ)"
printf 'Host: %s\n' "$(hostname)"
printf '\nDisk space:\n'
if command -v df >/dev/null 2>&1; then df -hP / || true; else echo '[warn] df unavailable'; fi
printf '\nMemory:\n'
if command -v free >/dev/null 2>&1; then free -h || true; else echo '[warn] free unavailable'; fi
printf '\nDocker daemon:\n'
if ! command -v docker >/dev/null 2>&1; then
  echo '[blocker] docker CLI unavailable'
  ((blockers+=1))
elif docker info --format '{{.ServerVersion}}' >/dev/null 2>&1; then
  echo '[ ok ] Docker daemon responds'
else
  echo '[blocker] Docker daemon unavailable or permission denied'
  ((blockers+=1))
fi
printf '\nDNS resolution:\n'
if command -v getent >/dev/null 2>&1; then
  for host in github.com ghcr.io; do
    if getent ahosts "$host" >/dev/null 2>&1; then
      printf '[ ok ] %s resolves\n' "$host"
    else
      printf '[blocker] %s does not resolve\n' "$host"
      ((blockers+=1))
    fi
  done
else
  echo '[blocker] getent unavailable; required DNS checks could not run'
  ((blockers+=1))
fi
printf '\nNo credentials, container environment, tokens, or application logs collected.\n'

printf '\nReadiness blockers: %d\n' "$blockers"
if ((blockers > 0)); then
  echo '[fail] recovery preflight blocked; resolve the conditions above before proceeding'
  exit 1
fi
echo '[ ok ] recovery preflight passed (not proof of RunnerHub job acceptance)'
