#!/usr/bin/env bash
set -euo pipefail
umask 077

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
version=""
evidence_dir=""
usage() {
  cat <<'EOF'
Usage: scripts/publish-recovery-images.sh --version vX.Y.Z [--evidence DIR]

Builds and publishes all six RunnerHub runtime images for linux/amd64 and
linux/arm64 from a clean local release tag. It signs image digests, adds
build provenance, verifies the full image set, then promotes immutable
versioned GHCR tags. It does not use GitHub Actions or deploy to any host.

Required environment:
  RUNNERHUB_RECOVERY_GHCR_USERNAME  GHCR account with package write permission
  RUNNERHUB_RECOVERY_GHCR_TOKEN     short-lived GHCR package write token
  RUNNERHUB_RECOVERY_SIGNING_KEY    protected cosign private-key file

The full local release validation runs before publishing. Recovery tags remain
if a build fails; version tags are promoted only after all six images verify.
EOF
}
fail() { printf '[fail] %s\n' "$*" >&2; exit 1; }
while (($#)); do
  case "$1" in
    --version) (($# >= 2)) || fail '--version requires a value'; version="$2"; shift 2 ;;
    --evidence) (($# >= 2)) || fail '--evidence requires a directory'; evidence_dir="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; fail "unknown argument: $1" ;;
  esac
done
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]] || fail 'a version tag like v1.2.3 is required'
[[ -n "${RUNNERHUB_RECOVERY_GHCR_USERNAME:-}" ]] || fail 'RUNNERHUB_RECOVERY_GHCR_USERNAME is required'
[[ -n "${RUNNERHUB_RECOVERY_GHCR_TOKEN:-}" ]] || fail 'RUNNERHUB_RECOVERY_GHCR_TOKEN is required'
key="${RUNNERHUB_RECOVERY_SIGNING_KEY:-}"
[[ -n "$key" && -f "$key" ]] || fail 'RUNNERHUB_RECOVERY_SIGNING_KEY must name a cosign key file'
for command in docker git crane cosign jq stat sha256sum realpath; do
  command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done
key="$(realpath -- "$key")"
docker buildx version >/dev/null 2>&1 || fail 'Docker Buildx is required'

cd "$repo_root"
git diff --quiet && git diff --cached --quiet || fail 'working tree has tracked changes'
[[ -z "$(git ls-files --others --exclude-standard)" ]] || fail 'working tree has untracked files'
source_sha="$(git rev-parse --verify HEAD^{commit})"
tag_sha="$(git rev-parse --verify "refs/tags/${version}^{commit}" 2>/dev/null || true)"
[[ "$tag_sha" == "$source_sha" ]] || fail "HEAD must exactly match local release tag $version"
if [[ -z "$evidence_dir" ]]; then
  evidence_dir="${TMPDIR:-/tmp}/runnerhub-recovery-evidence-${version}-${BASHPID}-${RANDOM}"
fi
evidence_dir="$(realpath -m -- "$evidence_dir")"
case "$evidence_dir/" in
  "$repo_root/"*) fail 'evidence directory must be outside the repository build context' ;;
esac
[[ ! -e "$evidence_dir" && ! -L "$evidence_dir" ]] || fail "evidence directory already exists: $evidence_dir"
mkdir -m 700 -p -- "$evidence_dir"
: > "$evidence_dir/publisher.log"
chmod 600 "$evidence_dir/publisher.log"
exec > >(tee "$evidence_dir/publisher.log") 2>&1

source_short="${source_sha:0:12}"
started_at="$(date -u +%FT%TZ)"
run_id="${started_at//[^0-9]/}-${BASHPID}-${RANDOM}"
recovery_tag="recovery-${source_short}-${run_id}"
[[ "$recovery_tag" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || fail 'generated recovery tag is invalid'
printf 'RunnerHub independent recovery publisher\nUTC start: %s\nSource commit: %s\nVersion: %s\n' "$started_at" "$source_sha" "$version"
printf '[note] This command does not deploy images or prove live RunnerHub acceptance.\n'
tmp="$(mktemp -d "${TMPDIR:-/tmp}/runnerhub-recovery.XXXXXX")"
chmod 700 "$tmp"
trap 'rm -rf -- "$tmp"' EXIT
export DOCKER_CONFIG="$tmp/docker-config"
mkdir -m 700 "$DOCKER_CONFIG"

printf '\n==> Full local release validation\n'
bash scripts/validate-release-local.sh --full --skip-runtime-builds

key_mode="$(stat -c '%a' "$key")"
(( (8#$key_mode & 077) == 0 )) || fail 'cosign key file must not be group/world accessible'
cosign public-key --key "$key" > "$tmp/recovery.pub"
key_fingerprint="$(sha256sum "$tmp/recovery.pub" | awk '{print $1}')"
printf '\n==> GHCR login (credentials stored only in a temporary private config)\n'
printf '%s' "$RUNNERHUB_RECOVERY_GHCR_TOKEN" | crane auth login ghcr.io \
  --username "$RUNNERHUB_RECOVERY_GHCR_USERNAME" --password-stdin >/dev/null
unset RUNNERHUB_RECOVERY_GHCR_TOKEN

targets=(control agent bootstrap scaleset deployer updater)
images=(runnerhub-control runnerhub-agent runnerhub-bootstrap runnerhub-scaleset runnerhub-deployer runnerhub-agent-updater)
digests=()
records=()
for index in "${!targets[@]}"; do
  target="${targets[$index]}"
  image="${images[$index]}"
  ref="ghcr.io/eutelic/${image}"
  recovery_ref="${ref}:${recovery_tag}"
  metadata="$tmp/${target}-metadata.json"
  predicate="$tmp/${target}-provenance.json"
  printf '\n==> Build and push %s (linux/amd64,linux/arm64)\n' "$image"
  docker buildx build --platform linux/amd64,linux/arm64 \
    --file deploy/docker/Dockerfile --target "$target" \
    --build-arg "VERSION=$source_sha" \
    --label "org.opencontainers.image.source=https://github.com/eutelic/runnerhub" \
    --label "org.opencontainers.image.revision=$source_sha" \
    --provenance=true --sbom=true --push --metadata-file "$metadata" \
    --tag "$recovery_ref" .
  digest="$(jq -r '.["containerimage.digest"] // empty' "$metadata")"
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid Buildx digest for $image"
  [[ "$(crane digest "$recovery_ref")" == "$digest" ]] || fail "registry digest mismatch for $image"
  manifest="$(crane manifest "$recovery_ref")"
  jq -e '[.manifests[].platform | select(.os == "linux" and .architecture == "amd64")] | length > 0' <<<"$manifest" >/dev/null || fail "$image is missing linux/amd64"
  jq -e '[.manifests[].platform | select(.os == "linux" and .architecture == "arm64")] | length > 0' <<<"$manifest" >/dev/null || fail "$image is missing linux/arm64"
  jq -e '[.manifests[] | select(.annotations["vnd.docker.reference.type"] == "attestation-manifest")] | length > 0' <<<"$manifest" >/dev/null || fail "$image is missing BuildKit SBOM/provenance attestations"
  jq -n --arg source "$source_sha" --arg version "$version" --arg target "$target" --arg started "$started_at" --arg finished "$(date -u +%FT%TZ)" \
    '{buildDefinition:{buildType:"https://eutelic/runnerhub/recovery-build/v1",externalParameters:{source:{uri:"https://github.com/eutelic/runnerhub",digest:{gitCommit:$source}},releaseVersion:$version,target:$target},internalParameters:{}},runDetails:{builder:{id:"scripts/publish-recovery-images.sh"},metadata:{invocationId:$source,startedOn:$started,finishedOn:$finished}}}' > "$predicate"
  cosign sign --yes --key "$key" "${ref}@${digest}"
  cosign attest --yes --key "$key" --type slsaprovenance --predicate "$predicate" "${ref}@${digest}"
  cosign verify --key "$tmp/recovery.pub" "${ref}@${digest}" >/dev/null
  cosign verify-attestation --key "$tmp/recovery.pub" --type slsaprovenance "${ref}@${digest}" >/dev/null
  digests+=("$digest")
  records+=("$(jq -n --arg target "$target" --arg image "$ref" --arg digest "$digest" '{target:$target,image:$image,digest:$digest,platforms:["linux/amd64","linux/arm64"],cosignSigned:true,cosignProvenance:true,buildKitAttestations:true}')")
  printf '[ ok ] %s %s\n' "$image" "$digest"
done

# Only an explicit absent-manifest response means the tag is free. Transport,
# authentication and rate-limit failures cannot authorize an overwrite.
lookup_version_digest() {
  local ref="$1" existing
  if existing="$(crane digest "$ref" 2>"$tmp/registry-error")"; then
    [[ "$existing" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid existing digest for $ref"
    printf '%s' "$existing"
  elif grep -Eq 'MANIFEST_UNKNOWN|manifest unknown' "$tmp/registry-error"; then
    return 0
  else
    fail "cannot verify existing version tag $ref; registry lookup failed"
  fi
}

printf '\n==> Preflight every versioned tag before promotion\n'
for index in "${!images[@]}"; do
  ref="ghcr.io/eutelic/${images[$index]}"
  digest="${digests[$index]}"
  existing="$(lookup_version_digest "${ref}:${version}")"
  [[ -z "$existing" || "$existing" == "$digest" ]] || fail "refusing to overwrite ${ref}:${version} at another digest"
done

printf '\n==> Promote versioned tags after all six builds verify\n'
for index in "${!images[@]}"; do
  ref="ghcr.io/eutelic/${images[$index]}"
  digest="${digests[$index]}"
  existing="$(lookup_version_digest "${ref}:${version}")"
  [[ -n "$existing" ]] || crane tag "${ref}@${digest}" "$version"
  [[ "$(crane digest "${ref}:${version}")" == "$digest" ]] || fail "version tag digest mismatch for $ref"
done

printf '\n==> Check anonymous GHCR reads without the write credential\n'
mkdir -m 700 "$tmp/anonymous-config"
for image in "${images[@]}"; do
  DOCKER_CONFIG="$tmp/anonymous-config" crane manifest "ghcr.io/eutelic/${image}:${version}" >/dev/null || fail "anonymous read failed for ${image}:${version}"
done
jq -n --arg repository "eutelic/runnerhub" --arg sourceCommit "$source_sha" --arg releaseVersion "$version" \
  --arg signerKeySHA256 "$key_fingerprint" --arg startedAt "$started_at" --arg completedAt "$(date -u +%FT%TZ)" \
  --argjson images "$(printf '%s\n' "${records[@]}" | jq -s '.')" \
  '{repository:$repository,sourceCommit:$sourceCommit,releaseVersion:$releaseVersion,signerKeySHA256:$signerKeySHA256,startedAt:$startedAt,completedAt:$completedAt,images:$images,limitations:["Does not prove a TrueNAS install or rollback drill.","Does not prove post-recovery RunnerHub JIT acceptance."]}' > "$evidence_dir/evidence.json"
chmod 600 "$evidence_dir/evidence.json"
printf '\n[ ok ] published six-image recovery set; evidence: %s/evidence.json\n' "$evidence_dir"
