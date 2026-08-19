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
chmod 0600 "$root/secrets/enrollment-token" "$root/secrets/agent-secret" "$root/secrets/admin-key" "$root/secrets/scaleset-key" "$root/secrets/github-app.pem"
printf '%s\n' '{"hostId":"host-test"}' > "$root/agent/agent-credentials.json"

# Force the verifier down its no-host-Docker path even on CI machines that have
# a Docker CLI/daemon available. Direct Docker cleanup behavior has separate
# provider/runtime coverage.
cat > "$tmp/bin/docker" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod +x "$tmp/bin/docker"

port="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(('127.0.0.1', 0))
print(s.getsockname()[1])
s.close()
PY
)"

cat > "$tmp/server.py" <<'PY'
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ADMIN = os.environ['TEST_ADMIN']
SCALER = os.environ['TEST_SCALER']

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def send_json(self, status, value):
        body = json.dumps(value).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == '/healthz':
            self.send_json(200, {'ok': True})
            return

        if not self.path.startswith('/api/'):
            self.send_json(404, {'error': 'not found'})
            return

        auth = self.headers.get('Authorization', '')
        if auth == f'Bearer {ADMIN}':
            if self.path == '/api/v1/hosts':
                self.send_json(200, [{'id': 'host-test', 'provider': 'docker', 'state': 'online'}])
                return
            if self.path == '/api/v1/pools':
                self.send_json(200, [{'id': 'pool-docker', 'isolation': 'container-trusted', 'labels': ['runnerhub']}])
                return
            if self.path == '/api/v1/workers':
                self.send_json(200, [])
                return
        elif auth == f'Bearer {SCALER}':
            if self.path == '/api/v1/workers':
                self.send_json(200, [])
                return
            if self.path == '/api/v1/pools':
                self.send_json(200, [{'id': 'pool-docker'}])
                return
            self.send_json(403, {'error': 'forbidden'})
            return
        else:
            self.send_json(401, {'error': 'unauthorized'})
            return

        self.send_json(404, {'error': 'not found'})

server = ThreadingHTTPServer(('127.0.0.1', int(os.environ['TEST_PORT'])), Handler)
server.serve_forever()
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
[ "$ready" = "true" ] || { echo 'mock RunnerHub API did not start' >&2; exit 1; }

PATH="$tmp/bin:$PATH" bash -n "$verify"
PATH="$tmp/bin:$PATH" bash "$verify" "$root" "http://127.0.0.1:$port" > "$tmp/success.log"
grep -Fq '[ ok ] administrative API rejects unauthenticated requests' "$tmp/success.log"
grep -Fq '[ ok ] scale-set credential remains constrained to its expected API surface' "$tmp/success.log"
grep -Fq '[ ok ] RunnerHub TrueNAS installation passed local verification' "$tmp/success.log"

chmod 0644 "$root/secrets/admin-key"
if PATH="$tmp/bin:$PATH" bash "$verify" "$root" "http://127.0.0.1:$port" >/dev/null 2>&1; then
  echo 'verifier unexpectedly accepted a generated secret with mode 0644' >&2
  exit 1
fi
chmod 0600 "$root/secrets/admin-key"

mv "$root/secrets/scaleset-key" "$root/secrets/scaleset-key.real"
ln -s "$root/secrets/scaleset-key.real" "$root/secrets/scaleset-key"
if PATH="$tmp/bin:$PATH" bash "$verify" "$root" "http://127.0.0.1:$port" >/dev/null 2>&1; then
  echo 'verifier unexpectedly accepted a symlinked generated secret' >&2
  exit 1
fi
rm "$root/secrets/scaleset-key"
mv "$root/secrets/scaleset-key.real" "$root/secrets/scaleset-key"
chmod 0600 "$root/secrets/scaleset-key"

printf 'TrueNAS verifier tests passed.\n'
