#!/usr/bin/env bash
set -euo pipefail

root="${1:-${RUNNERHUB_TRUENAS_ROOT:-/mnt/tank/runnerhub}}"
control_url="${2:-${RUNNERHUB_VERIFY_URL:-http://127.0.0.1:8787}}"
expected_pool_id="${3:-${RUNNERHUB_VERIFY_POOL_ID:-}}"
expected_label="${4:-${RUNNERHUB_VERIFY_SCALESET_NAME:-}}"
expected_isolation="${5:-${RUNNERHUB_VERIFY_ISOLATION:-}}"
github_key="${6:-${RUNNERHUB_GITHUB_APP_KEY_PATH:-$root/secrets/github-app.pem}}"
github_client_secret="${7:-${RUNNERHUB_GITHUB_CLIENT_SECRET_PATH:-$root/secrets/github-client-secret}}"

fail() { echo "[fail] $*" >&2; exit 1; }
ok() { echo "[ ok ] $*"; }
note() { echo "[note] $*"; }

if [[ -n "$expected_pool_id" ]]; then
  printf '%s' "$expected_pool_id" | grep -Eq '^[A-Za-z0-9._-]+$' || fail "expected pool ID contains unsupported characters: $expected_pool_id"
fi
if [[ -n "$expected_label" ]]; then
  printf '%s' "$expected_label" | grep -Eq '^[A-Za-z0-9._-]+$' || fail "expected runner label contains unsupported characters: $expected_label"
fi
case "$expected_isolation" in
  ""|container-trusted|container-dind) ;;
  *) fail "expected isolation must be container-trusted or container-dind" ;;
esac
[[ "$github_key" == /* ]] || fail "GitHub App key path must be absolute: $github_key"
[[ "$github_client_secret" == /* ]] || fail "GitHub client secret path must be absolute: $github_client_secret"

for cmd in curl python3 stat; do
  command -v "$cmd" >/dev/null 2>&1 || fail "$cmd is required"
done

for dir in data agent secrets; do
  [ -d "$root/$dir" ] || fail "missing RunnerHub path: $root/$dir"
done
ok "persistent data, agent, and secrets paths exist"

read_protected() {
  local path="$1"
  if [ -r "$path" ]; then
    cat "$path"
  elif command -v sudo >/dev/null 2>&1; then
    sudo cat "$path"
  else
    return 1
  fi
}

stat_mode_protected() {
  local path="$1"
  if stat -c '%a' "$path" >/dev/null 2>&1; then
    stat -c '%a' "$path"
  elif command -v sudo >/dev/null 2>&1; then
    sudo stat -c '%a' "$path"
  else
    return 1
  fi
}

validate_generated_secret() {
  local name="$1"
  local path="$root/secrets/$name"
  local value mode
  [ ! -L "$path" ] || fail "generated secret must not be a symbolic link: $path"
  [ -f "$path" ] || fail "generated secret must be a regular file: $path"
  value="$(read_protected "$path" 2>/dev/null || true)"
  [ "${#value}" -eq 64 ] || fail "generated secret must contain exactly 64 hexadecimal characters: $path"
  case "$value" in
    *[!0-9a-fA-F]*) fail "generated secret contains non-hexadecimal characters: $path" ;;
  esac
  mode="$(stat_mode_protected "$path" 2>/dev/null || true)"
  [ "$mode" = "600" ] || fail "generated secret must have mode 0600: $path (found ${mode:-unknown})"
}

for secret in enrollment-token agent-secret admin-key scaleset-key; do
  validate_generated_secret "$secret"
done
ok "generated RunnerHub credentials are regular 0600 files with valid 256-bit hex values"

validate_external_secret() {
  local label="$1" path="$2"
  [ ! -L "$path" ] || fail "$label must not be a symbolic link: $path"
  [ -f "$path" ] || fail "$label must be a regular file: $path"
  local value
  value="$(read_protected "$path" 2>/dev/null || true)"
  [ -n "$value" ] || fail "$label is missing or empty: $path"
  ok "$label is present as a regular non-empty file: $path"
}

validate_external_secret "configured GitHub App key" "$github_key"
validate_external_secret "configured GitHub OAuth client secret" "$github_client_secret"

admin_key="$(read_protected "$root/secrets/admin-key" 2>/dev/null || true)"
scaleset_key="$(read_protected "$root/secrets/scaleset-key" 2>/dev/null || true)"
[ -n "$admin_key" ] || fail "cannot read generated admin key"
[ -n "$scaleset_key" ] || fail "cannot read generated scale-set key"
[ "$admin_key" != "$scaleset_key" ] || fail "admin and scale-set credentials must be distinct"
ok "generated administrative and scale-set credentials are readable and distinct"

curl --fail --silent --show-error "$control_url/healthz" >/dev/null || fail "control health check failed at $control_url/healthz"
ok "control plane is healthy"

auth_status="$(curl --fail --silent --show-error "$control_url/auth/github/status" || true)"
[ -n "$auth_status" ] || fail "GitHub authentication status endpoint is unavailable"
python3 - "$auth_status" <<'PY'
import json, sys
status=json.loads(sys.argv[1])
if not status.get("enabled"):
    raise SystemExit("[fail] GitHub browser authentication is not enabled")
print("[ ok ] GitHub browser authentication is enabled")
PY

unauth_status="$(curl --silent --output /dev/null --write-out '%{http_code}' "$control_url/api/v1/hosts" || true)"
[ "$unauth_status" = "401" ] || fail "unauthenticated API request should return 401, got ${unauth_status:-no status}"
ok "administrative API rejects unauthenticated requests"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
chmod 0700 "$tmp"
printf 'header = "Authorization: Bearer %s"\n' "$admin_key" > "$tmp/curl-admin.conf"
printf 'header = "Authorization: Bearer %s"\n' "$scaleset_key" > "$tmp/curl-scaleset.conf"
chmod 0600 "$tmp/curl-admin.conf" "$tmp/curl-scaleset.conf"
unset admin_key scaleset_key unauth_status

curl --config "$tmp/curl-admin.conf" --fail --silent --show-error "$control_url/api/v1/hosts" > "$tmp/hosts.json" || fail "authenticated hosts API failed"
curl --config "$tmp/curl-admin.conf" --fail --silent --show-error "$control_url/api/v1/pools" > "$tmp/pools.json" || fail "authenticated pools API failed"
curl --config "$tmp/curl-admin.conf" --fail --silent --show-error "$control_url/api/v1/workers" > "$tmp/workers.json" || fail "authenticated workers API failed"
curl --config "$tmp/curl-admin.conf" --fail --silent --show-error "$control_url/api/v1/scalesets/status" > "$tmp/scaleset-status.json" || fail "authenticated scale-set status API failed"
ok "admin credential can read hosts, pools, workers, and scale-set status"

curl --config "$tmp/curl-scaleset.conf" --fail --silent --show-error "$control_url/api/v1/workers" >/dev/null || fail "scale-set credential cannot read workers"
curl --config "$tmp/curl-scaleset.conf" --fail --silent --show-error "$control_url/api/v1/scalesets/configs" >/dev/null || fail "scale-set credential cannot read dynamic listener configs"
scaleset_hosts_status="$(curl --config "$tmp/curl-scaleset.conf" --silent --output /dev/null --write-out '%{http_code}' "$control_url/api/v1/hosts" || true)"
[ "$scaleset_hosts_status" = "403" ] || fail "scale-set credential should be forbidden from reading hosts, got ${scaleset_hosts_status:-no status}"
ok "scale-set credential remains constrained to its expected API surface"

python3 - "$tmp/hosts.json" "$tmp/pools.json" "$tmp/workers.json" "$tmp/scaleset-status.json" "$expected_pool_id" "$expected_label" "$expected_isolation" <<'PY'
import json, sys
hosts=json.load(open(sys.argv[1]))
pools=json.load(open(sys.argv[2]))
workers=json.load(open(sys.argv[3]))
statuses=json.load(open(sys.argv[4]))
pool_id,label,isolation=sys.argv[5:8]
online=[h for h in hosts if h.get("provider")=="docker" and h.get("state")=="online"]
if not online:
    raise SystemExit("[fail] no online Docker host is registered")
print(f"[ ok ] online Docker hosts: {len(online)}")
if pool_id:
    matches=[p for p in pools if p.get("id")==pool_id]
    if len(matches)!=1:
        raise SystemExit(f"[fail] expected exactly one pool with id {pool_id!r}, found {len(matches)}")
    pool=matches[0]
    labels=pool.get("labels") or []
    if label and label not in labels:
        raise SystemExit(f"[fail] expected pool {pool_id!r} to advertise runner label {label!r}; found {labels!r}")
    if isolation and pool.get("isolation")!=isolation:
        raise SystemExit(f"[fail] expected pool {pool_id!r} isolation {isolation!r}; found {pool.get('isolation')!r}")
    print(f"[ ok ] expected pool {pool_id}: isolation={pool.get('isolation')} runs-on={','.join(labels)} maxConcurrency={pool.get('maxConcurrency')} image={pool.get('image')}")
    runtime=[s for s in statuses if s.get("poolId")==pool_id]
    if len(runtime)!=1:
        raise SystemExit(f"[fail] expected one scale-set runtime status for {pool_id!r}, found {len(runtime)}")
    if runtime[0].get("state")!="ready":
        raise SystemExit(f"[fail] scale-set listener for {pool_id!r} is not ready: state={runtime[0].get('state')!r} detail={runtime[0].get('detail','')!r}")
    print(f"[ ok ] scale-set listener for {pool_id} is ready")
elif pools:
    by_pool={s.get("poolId"):s for s in statuses}
    summary=", ".join(f"{p.get('id')}={by_pool.get(p.get('id'),{}).get('state','unknown')}" for p in pools)
    print(f"[note] repositories already connected: {len(pools)} pool(s); listener states: {summary}")
else:
    print("[note] no repositories connected yet; finish setup from the RunnerHub dashboard")
print(f"[note] workers recorded by control plane: {len(workers)}")
PY

credential_file="$root/agent/agent-credentials.json"
credential_json="$(read_protected "$credential_file" 2>/dev/null || true)"
[ -n "$credential_json" ] || fail "agent credential file is missing or unreadable: $credential_file"
ok "Docker agent credential persisted"

if command -v docker >/dev/null 2>&1 && docker version >/dev/null 2>&1; then
  mapfile -t scaleset_containers < <(
    docker ps --filter "label=io.runnerhub.component=scaleset" --format '{{.ID}}'
  )
  [ "${#scaleset_containers[@]}" -eq 1 ] || fail "expected exactly one running scale-set manager, found ${#scaleset_containers[@]}"
  scaleset_health="$(docker inspect --format '{{.State.Health.Status}}' "${scaleset_containers[0]}" 2>/dev/null || true)"
  [ "$scaleset_health" = "healthy" ] || fail "scale-set manager is not healthy (status: ${scaleset_health:-unknown})"
  ok "dynamic scale-set manager is healthy"

  docker ps -a --filter label=io.runnerhub.worker --format '{{.Label "io.runnerhub.worker"}}|||{{.ID}}|||{{.Status}}' > "$tmp/containers.txt"
  python3 - "$tmp/workers.json" "$tmp/containers.txt" <<'PY'
import json, sys
workers={w.get("id"):w for w in json.load(open(sys.argv[1]))}
stale=[]
containers=[]
for raw in open(sys.argv[2]):
    raw=raw.rstrip("\n")
    if not raw: continue
    parts=raw.split("|||",2)
    if len(parts)!=3:
        stale.append(f"unparseable RunnerHub container record: {raw}")
        continue
    worker_id=parts[0]
    containers.append(raw)
    worker=workers.get(worker_id)
    if worker is None:
        stale.append(f"container for unknown worker {worker_id}: {raw}")
    elif worker.get("state")=="deleted":
        stale.append(f"container remains for deleted worker {worker_id}: {raw}")
if stale:
    print("[fail] stale RunnerHub containers detected:",file=sys.stderr)
    for item in stale: print(f"  {item}",file=sys.stderr)
    raise SystemExit(1)
print(f"[ ok ] no stale RunnerHub worker containers ({len(containers)} current container records)")
PY

  while IFS='|' read -r sidecar _ _ parent; do
    [ -n "$sidecar" ] || continue
    [ -n "$parent" ] || fail "DinD sidecar $sidecar is missing its parent label"
    docker inspect "$parent" >/dev/null 2>&1 || fail "orphan DinD sidecar $sidecar references missing runner $parent"
  done < <(docker ps -a --filter label=io.runnerhub.role=dind --format '{{.ID}}|||{{.Label "io.runnerhub.parent"}}')
  ok "no orphan Docker-in-Docker sidecars"
else
  note "host Docker CLI is unavailable; skipped direct scale-set-manager and stale-container checks"
fi

echo
ok "RunnerHub TrueNAS installation passed local verification"
