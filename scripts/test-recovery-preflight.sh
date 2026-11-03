#!/usr/bin/env bash
# Deterministic regression tests for preflight exit status.
set -euo pipefail
script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/recovery-preflight.sh"
bash -n "$script"
tmp="$(mktemp -d)"
cleanup() {
  local status=$?
  if (( status != 0 )); then
    printf 'Recovery preflight regression failed (exit %d). Mock case logs:\n' "$status" >&2
    for log in "$tmp"/*.log; do
      [[ -f "$log" ]] || continue
      printf '\n--- %s ---\n' "${log##*/}" >&2
      cat "$log" >&2
    done
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'printf "Test assertion failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
mkdir -p "$tmp/bin"
cat > "$tmp/bin/docker" <<'EOF'
#!/bin/sh
exit "${MOCK_DOCKER_STATUS:-0}"
EOF
cat > "$tmp/bin/getent" <<'EOF'
#!/bin/sh
case "${MOCK_DNS_FAILURE:-}" in
  all) exit 2 ;;
  ghcr) [ "$2" = ghcr.io ] && exit 2 ;;
esac
exit 0
EOF
chmod +x "$tmp/bin/docker" "$tmp/bin/getent"
# Provide a known environment without relying on the host Docker daemon or DNS.
run_case() {
  local expected="$1" name="$2" status=0
  shift 2
  env PATH="$tmp/bin:$PATH" "$@" bash "$script" >"$tmp/$name.log" 2>&1 || status=$?
  if [[ "$status" -ne "$expected" ]]; then
    cat "$tmp/$name.log" >&2
    printf '%s: expected exit %s, got %s\n' "$name" "$expected" "$status" >&2
    exit 1
  fi
}
run_case 0 healthy MOCK_DOCKER_STATUS=0 MOCK_DNS_FAILURE=
run_case 1 docker_failure MOCK_DOCKER_STATUS=1 MOCK_DNS_FAILURE=
run_case 1 dns_failure MOCK_DOCKER_STATUS=0 MOCK_DNS_FAILURE=ghcr
run_case 1 multiple_failures MOCK_DOCKER_STATUS=1 MOCK_DNS_FAILURE=all
grep -q 'Readiness blockers: 3' "$tmp/multiple_failures.log"
# The help path is independent of readiness; test it separately.
env PATH="$tmp/bin:$PATH" MOCK_DOCKER_STATUS=1 MOCK_DNS_FAILURE=all bash "$script" --help >"$tmp/help.log"
grep -q 'Usage:' "$tmp/help.log"
# A missing DNS inspection utility must not be treated as a successful check.
mkdir -p "$tmp/no-getent"
for tool in docker date hostname df free; do
  if [[ "$tool" == docker ]]; then
    cp "$tmp/bin/docker" "$tmp/no-getent/docker"
  else
    ln -s "$(command -v "$tool")" "$tmp/no-getent/$tool"
  fi
done
status=0
env PATH="$tmp/no-getent" MOCK_DOCKER_STATUS=0 /bin/bash "$script" >"$tmp/missing_getent.log" 2>&1 || status=$?
[[ "$status" -eq 1 ]] || { cat "$tmp/missing_getent.log" >&2; echo 'missing getent must fail closed' >&2; exit 1; }
grep -q 'getent unavailable' "$tmp/missing_getent.log"

# A missing Docker CLI must fail closed even when DNS is healthy.
mkdir -p "$tmp/no-docker"
# Use the mocked resolver: the missing-Docker test must not depend on
# external DNS availability or the host resolver configuration.
cp "$tmp/bin/getent" "$tmp/no-docker/getent"
for tool in date hostname df free; do
  ln -s "$(command -v "$tool")" "$tmp/no-docker/$tool"
done
status=0
env PATH="$tmp/no-docker" MOCK_DNS_FAILURE= /bin/bash "$script" >"$tmp/missing_docker.log" 2>&1 || status=$?
[[ "$status" -eq 1 ]] || { cat "$tmp/missing_docker.log" >&2; echo 'missing docker must fail closed' >&2; exit 1; }
grep -q 'docker CLI unavailable' "$tmp/missing_docker.log"
grep -q 'Readiness blockers: 1' "$tmp/missing_docker.log"

# Evidence output must retain failure details without exposing an overly permissive directory.
evidence="$tmp/evidence"
status=0
env PATH="$tmp/bin:$PATH" MOCK_DOCKER_STATUS=1 MOCK_DNS_FAILURE=ghcr bash "$script" --output "$evidence" >"$tmp/evidence-console.log" 2>&1 || status=$?
[[ "$status" -eq 1 ]] || { echo 'evidence run must preserve blocker exit status' >&2; exit 1; }
[[ -f "$evidence/preflight.txt" ]] || { echo 'missing evidence file' >&2; exit 1; }
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  grep -q 'Readiness blockers: 2' "$evidence/preflight.txt" && break
  sleep 0.1
done
grep -q 'Readiness blockers: 2' "$evidence/preflight.txt"
[[ "$(stat -c %a "$evidence")" == 700 ]] || { echo 'evidence directory must be private' >&2; exit 1; }
[[ "$(stat -c %a "$evidence/preflight.txt")" == 600 ]] || { echo 'evidence file must be private' >&2; exit 1; }
status=0
env PATH="$tmp/bin:$PATH" MOCK_DOCKER_STATUS=0 MOCK_DNS_FAILURE= bash "$script" --output "$evidence" >"$tmp/overwrite.log" 2>&1 || status=$?
[[ "$status" -eq 2 ]] || { echo 'existing evidence directory must be rejected' >&2; exit 1; }
grep -q 'refusing to overwrite' "$tmp/overwrite.log"
printf 'recovery preflight exit-status tests passed\n'
