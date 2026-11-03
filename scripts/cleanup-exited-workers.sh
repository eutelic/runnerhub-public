#!/usr/bin/env bash
# Scoped, opt-in cleanup of exited RunnerHub Docker workers.
set -euo pipefail
mode=dry-run
limit=100
offset=0
ack=false
while (($#)); do
  case "$1" in
    --apply) mode=apply ;;
    --ack-diagnostics) ack=true ;;
    --limit) shift; [[ $# -gt 0 && $1 =~ ^[1-9][0-9]*$ ]] || { echo 'invalid --limit' >&2; exit 2; }; limit=$1; if (( ${#limit} > 4 )) || (( 10#$limit > 1000 )); then echo '--limit must be at most 1000' >&2; exit 2; fi ;;
    --offset) shift; [[ $# -gt 0 && $1 =~ ^(0|[1-9][0-9]*)$ ]] || { echo 'invalid --offset' >&2; exit 2; }; offset=$1; if (( ${#offset} > 8 )) || (( 10#$offset > 10000000 )); then echo '--offset must be at most 10000000' >&2; exit 2; fi ;;
    --help) echo 'Usage: cleanup-exited-workers.sh [--apply --ack-diagnostics] [--limit N] [--offset N]'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done
if [[ $mode == apply && $ack != true ]]; then
  echo '--apply requires --ack-diagnostics: review and preserve job logs before deletion' >&2
  exit 2
fi
command -v docker >/dev/null || { echo 'docker CLI missing' >&2; exit 1; }
# Both role and worker labels are required. Never select containers by name alone.
# --no-trunc is essential: DinD parent labels store the full 64-character runner ID.
# A 12-character ps ID would miss dependent sidecars and permit unsafe removal.
if ! candidates=$(docker ps -aq --no-trunc --filter 'status=exited' --filter 'label=io.runnerhub.role=runner' --filter 'label=io.runnerhub.worker'); then
  echo 'Docker candidate enumeration failed; no containers removed' >&2
  exit 1
fi
mapfile -t ids <<< "$candidates"
# Empty Docker output is a valid no-op, not a malformed container ID.
if [[ -z $candidates ]]; then
  echo "processed=0 errors=0 mode=$mode"
  exit 0
fi
count=0
errors=0
examined=0
position=0
for id in "${ids[@]}"; do
  ((position+=1))
  (( position > offset )) || continue
  (( examined < limit )) || break
  ((examined+=1))
  [[ $id =~ ^[a-f0-9]{12,64}$ ]] || { echo "skipping invalid container ID" >&2; ((errors+=1)); continue; }
  # Reinspect immediately before deletion: a container may have restarted since ps.
  info=$(docker inspect --format '{{.State.Running}}|{{.State.Status}}|{{index .Config.Labels "io.runnerhub.role"}}|{{index .Config.Labels "io.runnerhub.worker"}}' "$id") || { echo "inspect failed: $id" >&2; ((errors+=1)); continue; }
  IFS='|' read -r running status role worker <<< "$info"
  [[ $running == false && $status == exited && $role == runner && -n $worker && $worker != '<no value>' ]] || { echo "skipping changed container: $id" >&2; continue; }
  # DinD sidecars share the runner network namespace. Do not remove their parent.
  sidecars=$(docker ps -aq --filter "label=io.runnerhub.parent=$id") || { echo "sidecar lookup failed: $id" >&2; ((errors+=1)); continue; }
  if [[ -n $sidecars ]]; then
    echo "skipping runner with dependent sidecar: $id" >&2
    continue
  fi
  printf '%s %s worker=%s\n' "$mode" "$id" "$worker"
  if [[ $mode == apply ]]; then
    # No force: refuse deletion if Docker sees a newly running container.
    if ! docker rm "$id"; then
      echo "remove failed: $id" >&2
      ((errors+=1))
      continue
    fi
  fi
  ((count+=1))
done
printf 'processed=%d errors=%d mode=%s\n' "$count" "$errors" "$mode"
(( errors == 0 ))
