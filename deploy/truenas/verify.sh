#!/usr/bin/env bash
set -euo pipefail

root="${1:-${RUNNERHUB_TRUENAS_ROOT:-/mnt/tank/runnerhub}}"
control_url="${2:-${RUNNERHUB_VERIFY_URL:-http://127.0.0.1:8787}}"

fail() {
  echo "[fail] $*" >&2
  exit 1
}

ok() {
  echo "[ ok ] $*"
}

note() {
  echo "[note] $*"
}

for cmd in curl python3; do
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

admin_key="$(read_protected "$root/secrets/admin-key" 2>/dev/null || true)"
[ -n "$admin_key" ] || fail "cannot read generated admin key at $root/secrets/admin-key (run with sudo if needed)"
ok "generated admin credential is readable"

for secret in enrollment-token agent-secret admin-key scaleset-key github-app.pem; do
  value="$(read_protected "$root/secrets/$secret" 2>/dev/null || true)"
  [ -n "$value" ] || fail "missing or empty secret: $root/secrets/$secret"
done
ok "all persisted RunnerHub credentials and GitHub App key are present"

curl --fail --silent --show-error "$control_url/healthz" >/dev/null || fail "control health check failed at $control_url/healthz"
ok "control plane is healthy"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

auth_header="Authorization: Bearer $admin_key"
curl --fail --silent --show-error -H "$auth_header" "$control_url/api/v1/hosts" > "$tmp/hosts.json" || fail "authenticated hosts API failed"
curl --fail --silent --show-error -H "$auth_header" "$control_url/api/v1/pools" > "$tmp/pools.json" || fail "authenticated pools API failed"
curl --fail --silent --show-error -H "$auth_header" "$control_url/api/v1/workers" > "$tmp/workers.json" || fail "authenticated workers API failed"

python3 - "$tmp/hosts.json" "$tmp/pools.json" "$tmp/workers.json" <<'PY'
import json
import sys

hosts = json.load(open(sys.argv[1]))
pools = json.load(open(sys.argv[2]))
workers = json.load(open(sys.argv[3]))

online = [h for h in hosts if h.get("provider") == "docker" and h.get("state") == "online"]
if not online:
    raise SystemExit("[fail] no online Docker host is registered")
if not pools:
    raise SystemExit("[fail] no RunnerHub pool is configured")

print(f"[ ok ] online Docker hosts: {len(online)}")
print(f"[ ok ] configured pools: {len(pools)}")
print(f"[note] workers recorded by control plane: {len(workers)}")
for pool in pools:
    labels = ",".join(pool.get("labels") or [])
    print(f"[note] pool {pool.get('id')}: isolation={pool.get('isolation')} runs-on={labels}")
PY

credential_file="$root/agent/agent-credentials.json"
[ -s "$credential_file" ] || fail "agent credential file is missing: $credential_file"
ok "Docker agent credential persisted"

if command -v docker >/dev/null 2>&1 && docker version >/dev/null 2>&1; then
  docker ps -a --filter label=io.runnerhub.worker --format '{{.Label "io.runnerhub.worker"}}\t{{.ID}}\t{{.Status}}' > "$tmp/containers.tsv"

  python3 - "$tmp/workers.json" "$tmp/containers.tsv" <<'PY'
import json
import sys

workers = {w.get("id"): w for w in json.load(open(sys.argv[1]))}
stale = []
containers = []
for raw in open(sys.argv[2]):
    raw = raw.rstrip("\n")
    if not raw:
        continue
    parts = raw.split("\t", 2)
    worker_id = parts[0]
    containers.append(raw)
    worker = workers.get(worker_id)
    if worker is None:
        stale.append(f"container for unknown worker {worker_id}: {raw}")
    elif worker.get("state") == "deleted":
        stale.append(f"container remains for deleted worker {worker_id}: {raw}")

if stale:
    print("[fail] stale RunnerHub containers detected:", file=sys.stderr)
    for item in stale:
        print(f"  {item}", file=sys.stderr)
    raise SystemExit(1)

print(f"[ ok ] no stale RunnerHub worker containers ({len(containers)} current container records)")
PY

  while IFS=$'\t' read -r sidecar parent; do
    [ -n "$sidecar" ] || continue
    [ -n "$parent" ] || fail "DinD sidecar $sidecar is missing its parent label"
    docker inspect "$parent" >/dev/null 2>&1 || fail "orphan DinD sidecar $sidecar references missing runner $parent"
  done < <(docker ps -a --filter label=io.runnerhub.role=dind --format '{{.ID}}\t{{.Label "io.runnerhub.parent"}}')
  ok "no orphan Docker-in-Docker sidecars"
else
  note "host Docker CLI is unavailable; skipped direct stale-container checks"
fi

echo
ok "RunnerHub TrueNAS installation passed local verification"
