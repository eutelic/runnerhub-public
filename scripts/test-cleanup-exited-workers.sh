#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
cat > "$tmp/bin/docker" <<'MOCK'
#!/usr/bin/env bash
case "$1" in
  ps)
    if [[ "${FAIL_ENUMERATION:-0}" == 1 && " $* " != *'label=io.runnerhub.parent='* ]]; then
      echo 'mock Docker daemon unavailable' >&2
      exit 42
    fi
    if [[ "${EMPTY_ENUMERATION:-0}" == 1 && " $* " != *'label=io.runnerhub.parent='* ]]; then
      exit 0
    fi
    if [[ " $* " == *'label=io.runnerhub.parent='* ]]; then
      if [[ "${FULL_IDS:-0}" == 1 ]]; then
        # Sidecar label contains the full ID; a truncated parent lookup must miss it.
        [[ " $* " == *"label=io.runnerhub.parent=$(printf 'a%.0s' {1..64})"* ]] && echo cccccccccccc
        exit 0
      fi
      if [[ "${FAIL_SIDECAR_LOOKUP:-0}" == 1 ]]; then exit 42; fi
      [[ "${SIDECAR:-0}" == 1 ]] && echo cccccccccccc
      exit 0
    fi
    if [[ "${BULK_COUNT:-0}" != 0 ]]; then
      # Model a TrueNAS-sized exited-worker backlog without spawning 3,601
      # real containers. IDs are distinct, full-length Docker-style hashes.
      for (( n=1; n<=BULK_COUNT; n++ )); do printf '%064x\n' "$n"; done
      exit 0
    fi
    if [[ "${FULL_IDS:-0}" == 1 ]]; then
      # Docker truncates ps IDs unless --no-trunc is supplied. Model both
      # behaviors so removing --no-trunc fails the sidecar safety regression.
      if [[ " $* " == *' --no-trunc '* ]]; then
        printf '%s\n' "$(printf 'a%.0s' {1..64})"
      else
        printf '%s\n' aaaaaaaaaaaa
      fi
    else
      printf '%s\n' aaaaaaaaaaaa bbbbbbbbbbbb
    fi
    ;;
  inspect)
    if [[ "${FAIL_INSPECT:-0}" == 1 ]]; then exit 42; fi
    if [[ "${BULK_COUNT:-0}" != 0 ]]; then
      echo 'false|exited|runner|bulk-worker'
      exit 0
    fi
    if [[ " $* " == *bbbbbbbbbbbb* ]]; then
      if [[ "${SECOND_ELIGIBLE:-0}" == 1 ]]; then echo 'false|exited|runner|w2'; else echo 'true|running|runner|w2'; fi
    else
      if [[ "${FIRST_INELIGIBLE:-0}" == 1 ]]; then echo 'true|running|runner|w1'; else echo 'false|exited|runner|w1'; fi
    fi
    ;;
  rm)
    if [[ "${FAIL_REMOVE:-0}" == 1 ]]; then
      echo "mock Docker removal failure" >&2
      exit 42
    fi
    echo "$2" >> "$REMOVED"
    ;;
  *) exit 1 ;;
esac
MOCK
chmod +x "$tmp/bin/docker"
export PATH="$tmp/bin:$PATH" REMOVED="$tmp/removed"
script="$root/cleanup-exited-workers.sh"
bash -n "$script"
out="$(bash "$script")"
[[ "$out" == *'dry-run aaaaaaaaaaaa worker=w1'* ]]
[[ "$out" == *'processed=1 errors=0 mode=dry-run'* ]]
[[ ! -e "$REMOVED" ]]
if bash "$script" --apply >"$tmp/out" 2>&1; then
  echo 'apply without diagnostics acknowledgment succeeded' >&2
  exit 1
fi
[[ ! -e "$REMOVED" ]]
out="$(bash "$script" --apply --ack-diagnostics)"
[[ "$out" == *'processed=1 errors=0 mode=apply'* ]]
[[ "$(cat "$REMOVED")" == aaaaaaaaaaaa ]]
: > "$REMOVED"
export SIDECAR=1
out="$(bash "$script" --apply --ack-diagnostics)"
[[ "$out" == *'processed=0 errors=0 mode=apply'* ]]
[[ ! -s "$REMOVED" ]]
unset SIDECAR
# The provider labels DinD with the full 64-character runner ID.
# Ensure --no-trunc is passed to Docker and the parent cannot be deleted.
export FULL_IDS=1
: > "$REMOVED"
out="$(bash "$script" --apply --ack-diagnostics)"
[[ "$out" == *'processed=0 errors=0 mode=apply'* ]]
[[ ! -s "$REMOVED" ]]
unset FULL_IDS
export FAIL_ENUMERATION=1
if bash "$script" --apply --ack-diagnostics >"$tmp/fail-out" 2>&1; then
  echo 'failed Docker enumeration was treated as success' >&2
  exit 1
fi
grep -q 'Docker candidate enumeration failed' "$tmp/fail-out"
[[ ! -s "$REMOVED" ]]
unset FAIL_ENUMERATION
export EMPTY_ENUMERATION=1
out="$(bash "$script" --apply --ack-diagnostics)"
[[ "$out" == *'processed=0 errors=0 mode=apply'* ]]
[[ ! -s "$REMOVED" ]]
unset EMPTY_ENUMERATION
for failure in FAIL_INSPECT FAIL_SIDECAR_LOOKUP; do
  export "$failure=1"
  : > "$REMOVED"
  if bash "$script" --apply --ack-diagnostics >"$tmp/fail-out" 2>&1; then
    echo "$failure was treated as successful cleanup" >&2
    exit 1
  fi
  grep -q 'errors=1' "$tmp/fail-out" || grep -q 'errors=2' "$tmp/fail-out"
  [[ ! -s "$REMOVED" ]]
  unset "$failure"
done
export FAIL_REMOVE=1
: > "$REMOVED"
if bash "$script" --apply --ack-diagnostics >"$tmp/fail-out" 2>&1; then
  echo 'failed Docker removal was treated as successful cleanup' >&2
  exit 1
fi
grep -q 'remove failed: aaaaaaaaaaaa' "$tmp/fail-out"
grep -q 'processed=0 errors=1 mode=apply' "$tmp/fail-out"
[[ ! -s "$REMOVED" ]]
unset FAIL_REMOVE
# Limit bounds Docker inspection attempts, not just successful removals.
: > "$REMOVED"
out="$(bash "$script" --apply --ack-diagnostics --limit 1)"
[[ "$out" == *'processed=1 errors=0 mode=apply'* ]]
[[ "$(cat "$REMOVED")" == aaaaaaaaaaaa ]]
# A skipped candidate must still consume the inspection budget.
export FIRST_INELIGIBLE=1 SECOND_ELIGIBLE=1
: > "$REMOVED"
out="$(bash "$script" --apply --ack-diagnostics --limit 1)"
[[ "$out" == *'processed=0 errors=0 mode=apply'* ]]
[[ ! -s "$REMOVED" ]]
unset FIRST_INELIGIBLE SECOND_ELIGIBLE
# An explicit offset lets operators reach later candidates when early ones
# are permanently ineligible; the inspection budget still applies.
export FIRST_INELIGIBLE=1 SECOND_ELIGIBLE=1
: > "$REMOVED"
out="$(bash "$script" --apply --ack-diagnostics --offset 1 --limit 1)"
[[ "$out" == *'processed=1 errors=0 mode=apply'* ]]
[[ "$(cat "$REMOVED")" == bbbbbbbbbbbb ]]
unset FIRST_INELIGIBLE SECOND_ELIGIBLE
# Invalid offsets must not trigger removal.
: > "$REMOVED"
if bash "$script" --apply --ack-diagnostics --offset -1 >"$tmp/fail-out" 2>&1; then
  echo 'invalid offset accepted' >&2
  exit 1
fi
[[ ! -s "$REMOVED" ]]
# Reject oversized numeric arguments before Bash arithmetic can wrap or cause
# unexpectedly unbounded Docker operations.
for args in '--limit 1001' '--limit 999999999999999999999' '--offset 10000001' '--offset 999999999999999999999'; do
  : > "$REMOVED"
  # Intentional word splitting for this fixed test matrix.
  if bash "$script" --apply --ack-diagnostics $args >"$tmp/fail-out" 2>&1; then
    echo "oversized numeric argument accepted: $args" >&2
    exit 1
  fi
  [[ ! -s "$REMOVED" ]]
done
# Empty enumeration uses the same machine-readable summary schema.
export EMPTY_ENUMERATION=1
out="$(bash "$script")"
[[ "$out" == *'processed=0 errors=0 mode=dry-run'* ]]
unset EMPTY_ENUMERATION
# The October TrueNAS incident involved 3,601 exited worker containers.
# Enumerate a comparable backlog but verify the tool performs bounded
# reinspection/deletion and never escalates its --limit or bypasses dry-run.
export BULK_COUNT=3601
: > "$REMOVED"
out="$(bash "$script" --limit 25)"
[[ "$out" == *'processed=25 errors=0 mode=dry-run'* ]]
[[ ! -s "$REMOVED" ]]
# Selection after a large offset is still a bounded window. An explicit
# offset is necessary because ineligible early containers may stay behind.
out="$(bash "$script" --offset 3500 --limit 25)"
[[ "$out" == *'processed=25 errors=0 mode=dry-run'* ]]
[[ ! -s "$REMOVED" ]]
out="$(bash "$script" --apply --ack-diagnostics --offset 3500 --limit 25)"
[[ "$out" == *'processed=25 errors=0 mode=apply'* ]]
[[ "$(wc -l < "$REMOVED")" -eq 25 ]]
[[ "$(head -n 1 "$REMOVED")" == "$(printf '%064x' 3501)" ]]
[[ "$(tail -n 1 "$REMOVED")" == "$(printf '%064x' 3525)" ]]
unset BULK_COUNT
echo 'scoped cleanup regression tests passed'
