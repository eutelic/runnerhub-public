#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
verify="$script_dir/verify.sh"

tmp="$(mktemp -d)"
server_pid=""
cleanup() {
  if [ -n "$server_pid" ]; then
    kill "$server_pid" >/dev/null 2>&1 || true
    wait "$server_pid" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

root="$tmp/runnerhub"
mkdir -p "$root/data" "$root/agent" "$root/secrets" "$tmp/bin"

admin_key="$(printf '%064d' 1)"
scaleset_key="$(printf '%064d' 2)"
enrollment_token="$(printf '%064d' 3)"
agent_secret="$(printf '%064d' 4)"

printf '%s\n' "$enrollment_token" > "$root/secrets/enrollment-token"
printf '%s\n' "$agent_secret" > "$root/secrets/agent-secret"
printf '%s\n' "$admin_key" > "$root/secrets/admin-key"
printf '%s\n' "$scaleset_key" > "$root/secrets/scaleset-key"
printf '%s\n' 'test-private-key' > "$root/secrets/github-app.pem"
printf '%s\n' 'test-client-secret' > "$root/secrets/github-client-secret"
chmod 0600 "$root/secrets/"*
printf '%s\n' '{"hostId":"host-test"}' > "$root/agent/agent-credentials.json"

cat > "$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
mode="${FAKE_DOCKER_MODE:-unavailable}"
case "${1:-}" in
  version)
    [ "$mode" != unavailable ] || exit 1
    exit 0
    ;;
  ps)
    [ "$mode" != unavailable ] || exit 1
    args=" $* "
    if [[ "$args" == *"label=io.runnerhub.component=scaleset"* ]]; then
      case "$mode" in
        healthy|unhealthy) printf '%s\n' scaleset-manager-ci ;;
        missing) : ;;
        duplicate) printf '%s\n' scaleset-manager-ci scaleset-manager-ci-2 ;;
        *) printf '%s\n' scaleset-manager-ci ;;
      esac
      exit 0
    fi
    exit 0
    ;;
  inspect)
    [ "$mode" != unavailable ] || exit 1
    if [[ " $* " == *"State.Health.Status"* ]]; then
      case "$mode" in
        unhealthy) printf '%s\n' unhealthy ;;
        *) printf '%s\n' healthy ;;
      esac
      exit 0
    fi
    exit 0
    ;;
  *) exit 1 ;;
esac
EOF
chmod +x "$tmp/bin/docker"

port="$(python3 - <<'PY'
import socket
s=socket.socket()
s.bind(('127.0.0.1',0))
print(s.getsockname()[1])
s.close()
PY
)"

cat > "$tmp/server.py" <<'PY'
import json, os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ADMIN=os.environ["TEST_ADMIN"]
SCALER=os.environ["TEST_SCALER"]

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args): pass
    def send_json(self,status,value):
        body=json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type","application/json")
        self.send_header("Content-Length",str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def do_GET(self):
        if self.path=="/healthz":
            self.send_json(200,{"status":"ok"}); return
        if self.path=="/auth/github/status":
            self.send_json(200,{"enabled":True,"authenticated":False}); return
        if not self.path.startswith("/api/"):
            self.send_json(404,{"error":"not found"}); return
        auth=self.headers.get("Authorization","")
        if auth==f"Bearer {ADMIN}":
            if self.path=="/api/v1/hosts":
                self.send_json(200,[{"id":"host-test","provider":"docker","state":"online"}]); return
            if self.path=="/api/v1/pools":
                mode=os.environ.get("TEST_POOLS","none")
                pools=[]
                if mode=="configured":
                    pools=[{"id":"pool-docker","isolation":"container-trusted","labels":["runnerhub"],"maxConcurrency":2,"image":"ghcr.io/actions/actions-runner:latest"}]
                self.send_json(200,pools); return
            if self.path=="/api/v1/workers":
                self.send_json(200,[]); return
        elif auth==f"Bearer {SCALER}":
            if self.path=="/api/v1/workers":
                self.send_json(200,[]); return
            if self.path=="/api/v1/pools":
                self.send_json(200,[]); return
            if self.path=="/api/v1/scalesets/configs":
                self.send_json(200,[]); return
            self.send_json(403,{"error":"forbidden"}); return
        else:
            self.send_json(401,{"error":"unauthorized"}); return
        self.send_json(404,{"error":"not found"})

ThreadingHTTPServer(("127.0.0.1",int(os.environ["TEST_PORT"])),Handler).serve_forever()
PY

TEST_ADMIN="$admin_key" TEST_SCALER="$scaleset_key" TEST_PORT="$port" python3 "$tmp/server.py" &
server_pid=$!

ready=false
for _ in $(seq 1 50); do
  if curl --fail --silent "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.1
done
[ "$ready" = true ] || { echo 'mock RunnerHub API did not start' >&2; exit 1; }

PATH="$tmp/bin:$PATH" bash -n "$verify"

PATH="$tmp/bin:$PATH" bash "$verify" "$root" "http://127.0.0.1:$port" > "$tmp/pre-onboarding.log"
grep -Fq '[ ok ] GitHub browser authentication is enabled' "$tmp/pre-onboarding.log"
grep -Fq '[note] no repositories connected yet; finish setup from the RunnerHub dashboard' "$tmp/pre-onboarding.log"
grep -Fq '[ ok ] scale-set credential remains constrained to its expected API surface' "$tmp/pre-onboarding.log"
grep -Fq '[ ok ] RunnerHub TrueNAS installation passed local verification' "$tmp/pre-onboarding.log"

FAKE_DOCKER_MODE=healthy PATH="$tmp/bin:$PATH"   bash "$verify" "$root" "http://127.0.0.1:$port" > "$tmp/docker-success.log"
grep -Fq '[ ok ] dynamic scale-set manager is healthy' "$tmp/docker-success.log"
grep -Fq '[ ok ] no stale RunnerHub worker containers (0 current container records)' "$tmp/docker-success.log"
grep -Fq '[ ok ] no orphan Docker-in-Docker sidecars' "$tmp/docker-success.log"

for mode in unhealthy missing duplicate; do
  if FAKE_DOCKER_MODE="$mode" PATH="$tmp/bin:$PATH"     bash "$verify" "$root" "http://127.0.0.1:$port" >/dev/null 2>&1; then
    echo "verifier unexpectedly accepted scale-set manager Docker mode: $mode" >&2
    exit 1
  fi
done

kill "$server_pid"
wait "$server_pid" >/dev/null 2>&1 || true
server_pid=""
TEST_ADMIN="$admin_key" TEST_SCALER="$scaleset_key" TEST_PORT="$port" TEST_POOLS=configured python3 "$tmp/server.py" &
server_pid=$!
for _ in $(seq 1 50); do
  curl --fail --silent "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done
PATH="$tmp/bin:$PATH" bash "$verify" "$root" "http://127.0.0.1:$port" pool-docker runnerhub container-trusted > "$tmp/post-onboarding.log"
grep -Fq '[ ok ] expected pool pool-docker: isolation=container-trusted runs-on=runnerhub maxConcurrency=2 image=ghcr.io/actions/actions-runner:latest' "$tmp/post-onboarding.log"

managed_dir="$tmp/scale-secrets/secrets"
mkdir -p "$managed_dir"
printf '%s\n' managed-test-private-key > "$managed_dir/runnerhub_github_app"
printf '%s\n' managed-test-client-secret > "$managed_dir/runnerhub_github_client_secret"
chmod 0444 "$managed_dir/"*
PATH="$tmp/bin:$PATH" bash "$verify"   "$root" "http://127.0.0.1:$port" "" "" ""   "$managed_dir/runnerhub_github_app" "$managed_dir/runnerhub_github_client_secret" > "$tmp/managed.log"
grep -Fq "[ ok ] configured GitHub App key is present as a regular non-empty file: $managed_dir/runnerhub_github_app" "$tmp/managed.log"
grep -Fq "[ ok ] configured GitHub OAuth client secret is present as a regular non-empty file: $managed_dir/runnerhub_github_client_secret" "$tmp/managed.log"

chmod 0644 "$root/secrets/admin-key"
if PATH="$tmp/bin:$PATH" bash "$verify" "$root" "http://127.0.0.1:$port" >/dev/null 2>&1; then
  echo 'verifier unexpectedly accepted a generated secret with mode 0644' >&2
  exit 1
fi
chmod 0600 "$root/secrets/admin-key"

printf 'TrueNAS verifier tests passed.\n'
