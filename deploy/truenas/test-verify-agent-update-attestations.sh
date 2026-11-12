#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
validator="$repo_root/deploy/truenas/verify-agent-update-attestations.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
chmod 0700 "$tmp"

cat > "$tmp/cosign" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$COSIGN_FAKE_LOG"
[[ "$1" == verify ]] || exit 3
[[ "${COSIGN_FAKE_FAILURE:-false}" != true ]] || exit 1
ref="${*: -1}"
digest="${ref##*@}"
if [[ "${COSIGN_FAKE_WRONG_SUBJECT:-false}" == true ]]; then
  digest="sha256:$(printf '0%.0s' {1..64})"
fi
rev="$TEST_SOURCE_REV"
if [[ "${COSIGN_FAKE_WRONG_SHA:-false}" == true ]]; then
  rev="$(printf '0%.0s' {1..40})"
fi
printf '[{"critical":{"image":{"docker-manifest-digest":"%s"}},"optional":{"source-sha":"%s"}}]' "$digest" "$rev"
MOCK
chmod 0755 "$tmp/cosign"
export PATH="$tmp:$PATH"
export COSIGN_FAKE_LOG="$tmp/cosign-calls"

revision="$(printf 'a%.0s' {1..40})"
agent="ghcr.io/eutelic/runnerhub-agent@sha256:$(printf 'b%.0s' {1..64})"
helper="ghcr.io/eutelic/runnerhub-agent-updater@sha256:$(printf 'c%.0s' {1..64})"
export TEST_SOURCE_REV="$revision"

bash "$validator" "$revision" "$agent" "$helper" >/dev/null
test "$(wc -l < "$COSIGN_FAKE_LOG")" -eq 2
grep -Fq -- '--certificate-oidc-issuer https://token.actions.githubusercontent.com' "$COSIGN_FAKE_LOG"
grep -Fq -- '--certificate-identity-regexp' "$COSIGN_FAKE_LOG"
grep -Fq -- 'publish-images' "$COSIGN_FAKE_LOG"
grep -Fq -- '--certificate-github-workflow-repository eutelic/runnerhub' "$COSIGN_FAKE_LOG"
grep -Fq -- "--certificate-github-workflow-sha $revision" "$COSIGN_FAKE_LOG"
grep -Fq -- "--annotations source-sha=$revision" "$COSIGN_FAKE_LOG"
grep -Fq -- "$agent" "$COSIGN_FAKE_LOG"
grep -Fq -- "$helper" "$COSIGN_FAKE_LOG"

if COSIGN_FAKE_WRONG_SUBJECT=true bash "$validator" "$revision" "$agent" "$helper" >/dev/null 2>&1; then
  echo '[fail] fake success with mismatched signed digest was accepted' >&2
  exit 1
fi
if COSIGN_FAKE_WRONG_SHA=true bash "$validator" "$revision" "$agent" "$helper" >/dev/null 2>&1; then
  echo '[fail] fake success with mismatched signed source SHA was accepted' >&2
  exit 1
fi
if COSIGN_FAKE_FAILURE=true bash "$validator" "$revision" "$agent" "$helper" >/dev/null 2>&1; then
  echo '[fail] failed signature verification was accepted' >&2
  exit 1
fi
if bash "$validator" "bad-commit" "$agent" "$helper" >/dev/null 2>&1; then
  echo '[fail] malformed source commit was accepted' >&2
  exit 1
fi
if bash "$validator" "$revision" 'ghcr.io/other/agent@sha256:bad' "$helper" >/dev/null 2>&1; then
  echo '[fail] untrusted release image was accepted' >&2
  exit 1
fi

# The release must not revert to the unavailable GitHub attestation API.
workflow="$repo_root/.github/workflows/publish-images.yml"
grep -Fq 'sigstore/cosign-installer@' "$workflow"
grep -Fq 'cosign sign --yes' "$workflow"
grep -Fq 'cosign verify' "$workflow"
grep -Fq 'DOCKER_BUILD_RECORD_UPLOAD: "false"' "$workflow"
if grep -Fq 'uses: actions/attest@' "$workflow"; then
  echo '[fail] paid GitHub artifact-attestation action reintroduced' >&2
  exit 1
fi

echo '[ok] agent-update Cosign signature verifier fail-closed tests passed'
