#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
full=false
registry_tag=""
keep_images=false

usage() {
  cat <<'EOF'
Usage:
  bash scripts/validate-release-local.sh [--full] [--registry-tag TAG] [--keep-images]

Runs the deployment and TrueNAS validation that can be reproduced on a normal
Docker host without GitHub Actions credentials.

Default validation:
  - deployment shell syntax
  - TrueNAS generator/preflight/verifier/image-tool mock suites
  - development, release, and TrueNAS Compose parsing
  - native builds of control, agent, bootstrap, scale-set, and deployer runtime images
  - persisted control DB ownership/symlink migration smoke

--full additionally runs the core/scaleset Go test, race, soak, vet,
govulncheck, and agent cross-build checks inside the same Go 1.26.6 container
image used by production builds.

--registry-tag TAG additionally verifies that all five GHCR images for TAG are
readable and publish linux/amd64 + linux/arm64. Docker must already be logged in
when the packages are private.
EOF
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

step() {
  printf '\n==> %s\n' "$*"
}

while (($#)); do
  case "$1" in
    --full)
      full=true
      shift
      ;;
    --registry-tag)
      (($# >= 2)) || fail '--registry-tag requires a value'
      registry_tag="$2"
      shift 2
      ;;
    --keep-images)
      keep_images=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

for cmd in bash sh docker openssl python3 curl stat; do
  command -v "$cmd" >/dev/null 2>&1 || fail "$cmd is required"
done

docker version >/dev/null 2>&1 || fail 'Docker daemon is not reachable'
docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is required'

if [ -n "$registry_tag" ]; then
  [[ "$registry_tag" =~ ^[A-Za-z0-9_.-]+$ ]] || fail '--registry-tag contains unsupported characters'
fi

run_id="$(date +%s)-$$"
image_prefix="runnerhub-local-validation-$run_id"
images=(
  "$image_prefix-control"
  "$image_prefix-agent"
  "$image_prefix-bootstrap"
  "$image_prefix-scaleset"
  "$image_prefix-deployer"
)

tmp="$(mktemp -d "${RUNNER_TEMP:-/tmp}/runnerhub-release.XXXXXX")"
cleanup() {
  rm -rf "$tmp"
  if [ "$keep_images" != true ]; then
    docker image rm -f "${images[@]}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

cd "$repo_root"

step 'Validate deployment shell syntax'
sh -n deploy/docker/secret-file.sh
sh -n deploy/docker/control-entrypoint.sh
sh -n deploy/docker/agent-entrypoint.sh
sh -n deploy/docker/scaleset-entrypoint.sh
sh -n deploy/docker/bootstrap-entrypoint.sh
sh -n deploy/docker/init-secrets.sh
bash -n deploy/docker/test-control-data-ownership.sh
bash -n scripts/init-docker-env.sh
bash -n scripts/start-docker.sh
bash -n scripts/validate-release-local.sh
for script in deploy/truenas/*.sh; do
  bash -n "$script"
done

step 'Run self-contained TrueNAS deployment-tool tests'
bash deploy/truenas/test-prepare-compose.sh
bash deploy/truenas/test-preflight-host.sh
bash deploy/truenas/test-verify.sh
bash deploy/truenas/test-check-images.sh
bash deploy/truenas/test-verify-image-set.sh

step 'Validate Compose configurations'
envfile="$tmp/runnerhub.env"
keyfile="$tmp/github-app.pem"
clientsecretfile="$tmp/github-client-secret"
openssl genrsa -out "$keyfile" 2048 >/dev/null 2>&1
printf '%s\n' 'local-github-client-secret' > "$clientsecretfile"
chmod 0600 "$clientsecretfile"
printf '%s\n' \
  'RUNNERHUB_ENROLLMENT_TOKEN=local-enrollment' \
  'RUNNERHUB_AGENT_SECRET=local-agent-secret-012345678901234567890123456789' \
  'RUNNERHUB_ADMIN_KEY=local-admin' \
  'RUNNERHUB_SCALESET_KEY=local-scaler' \
  'RUNNERHUB_API_KEYS=local-admin:admin:local-admin,local-scaler:scaler:scaleset-listener' \
  'RUNNERHUB_GITHUB_APP_ID=123456789' \
  'RUNNERHUB_GITHUB_APP_CLIENT_ID=Iv1.local' \
  'RUNNERHUB_GITHUB_APP_INSTALLATION_ID=12345678' \
  "RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE=$keyfile" \
  "RUNNERHUB_GITHUB_CLIENT_SECRET_FILE=$clientsecretfile" \
  'RUNNERHUB_GITHUB_APP_SLUG=runnerhub-local' \
  'RUNNERHUB_PUBLIC_URL=http://127.0.0.1:8080' \
  'RUNNERHUB_GITHUB_URL=https://github.com/acme/app' > "$envfile"
chmod 0600 "$envfile"

docker compose --env-file "$envfile" -f deploy/docker/compose.yaml --profile autoscale config >/dev/null
docker compose --env-file "$envfile" -f deploy/docker/compose.release.yaml config >/dev/null
docker compose -f deploy/truenas/compose.yaml config >/dev/null
docker compose -f deploy/truenas/deployer.compose.yaml config >/dev/null

step 'Build all published runtime image targets'
version="local-validation"
if command -v git >/dev/null 2>&1; then
  version="$(git rev-parse --verify HEAD 2>/dev/null || printf local-validation)"
fi

docker build --build-arg "VERSION=$version" --target control -f deploy/docker/Dockerfile -t "${images[0]}" .
docker build --build-arg "VERSION=$version" --target agent -f deploy/docker/Dockerfile -t "${images[1]}" .
docker build --build-arg "VERSION=$version" --target bootstrap -f deploy/docker/Dockerfile -t "${images[2]}" .
docker build --build-arg "VERSION=$version" --target scaleset -f deploy/docker/Dockerfile -t "${images[3]}" .
docker build --build-arg "VERSION=$version" --target deployer -f deploy/docker/Dockerfile -t "${images[4]}" .

step 'Exercise persisted control-data ownership migration'
bash deploy/docker/test-control-data-ownership.sh "${images[0]}"

if [ "$full" = true ]; then
  step 'Run full core Go validation in production toolchain'
  docker run --rm \
    -v "$repo_root:/src:ro" \
    -w /src \
    golang:1.26.6-bookworm \
    bash -c '
      set -euo pipefail
      go test ./...
      go test -race ./internal/...
      go test -tags localtest ./tests/soak -count=1
      go vet ./...
      go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
      GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o /tmp/runnerhub-agent-darwin ./cmd/runnerhub-agent
      GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o /tmp/runnerhub-agent-windows.exe ./cmd/runnerhub-agent
    '

  step 'Run full scale-set Go validation in production toolchain'
  docker run --rm \
    -v "$repo_root:/src:ro" \
    -w /src/scaleset \
    golang:1.26.6-bookworm \
    bash -c '
      set -euo pipefail
      go test ./...
      go vet ./...
      go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
    '
fi

if [ -n "$registry_tag" ]; then
  step "Verify published GHCR image set: $registry_tag"
  bash deploy/truenas/check-images.sh --tag "$registry_tag"
fi

printf '\n[ ok ] RunnerHub local release validation passed\n'
if [ "$full" != true ]; then
  printf '[note] Re-run with --full for Go race/soak/vet/vulnerability validation\n'
fi
if [ -z "$registry_tag" ]; then
  printf '[note] Use --registry-tag TAG to validate a published GHCR release set\n'
fi
if [ "$keep_images" = true ]; then
  printf '[note] Validation images retained with prefix %s\n' "$image_prefix"
fi
