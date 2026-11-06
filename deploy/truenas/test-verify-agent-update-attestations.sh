#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
validator="$repo_root/deploy/truenas/verify-agent-update-attestations.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
chmod 0700 "$tmp"

cat > "$tmp/gh" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$GH_FAKE_LOG"
[ "$1" = attestation ] && [ "$2" = verify ] || exit 3
ref="$(printf '%s' "$3" | sed 's|^oci://||')"
name="$(printf '%s' "$ref" | cut -d@ -f1)"
digest="$(printf '%s' "$ref" | sed 's|.*@sha256:||')"
if [ "$GH_FAKE_WRONG_SUBJECT" = true ]; then
  digest="$(printf '0%.0s' {1..64})"
fi
printf '[{"verificationResult":{"statement":{"predicateType":"https://slsa.dev/provenance/v1","subject":[{"name":"%s","digest":{"sha256":"%s"}}]}}}]' "$name" "$digest"
MOCK
chmod 0755 "$tmp/gh"
export PATH="$tmp:$PATH"
export GH_FAKE_LOG="$tmp/gh-calls"
export GH_FAKE_WRONG_SUBJECT=false

revision="$(printf 'a%.0s' {1..40})"
agent="ghcr.io/eutelic/runnerhub-agent@sha256:$(printf 'b%.0s' {1..64})"
helper="ghcr.io/eutelic/runnerhub-agent-updater@sha256:$(printf 'c%.0s' {1..64})"

bash "$validator" "$revision" "$agent" "$helper" >/dev/null
test "$(wc -l < "$GH_FAKE_LOG")" -eq 2
grep -Fq -- '--repo eutelic/runnerhub' "$GH_FAKE_LOG"
grep -Fq -- '--signer-workflow eutelic/runnerhub/.github/workflows/publish-images.yml' "$GH_FAKE_LOG"
grep -Fq -- "--source-digest $revision" "$GH_FAKE_LOG"

if GH_FAKE_WRONG_SUBJECT=true bash "$validator" "$revision" "$agent" "$helper" >/dev/null 2>&1; then
  echo '[fail] fake success with mismatched attestation subject was accepted' >&2
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

echo '[ok] agent-update release provenance verifier fail-closed tests passed'
