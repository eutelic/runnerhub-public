#!/usr/bin/env bash
# Install RunnerHub's local stdio MCP with dashboard-approved pairing.
set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
URL="${1:-http://truenas-scale:8788}"
URL="${URL%/}"
BIN="${HOME}/.local/share/runnerhub-mcp/runnerhub-mcp"
LAUNCHER="${HOME}/.local/bin/runnerhub-mcp"

for tool in go curl codex; do
  command -v "$tool" >/dev/null || { echo "Missing required command: $tool" >&2; exit 1; }
done

if ! curl --fail --silent --show-error --max-time 10 \
    "${URL}/api/v1/mcp/pair/status" | grep -q '"status"'; then
  echo "RunnerHub pairing API is not reachable at ${URL}." >&2
  echo "Redeploy a RunnerHub image containing this change, then retry." >&2
  exit 1
fi

mkdir -p "$(dirname "$BIN")" "$(dirname "$LAUNCHER")"
(cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/runnerhub-mcp)

{
  printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail'
  printf 'export RUNNERHUB_URL=%q\n' "$URL"
  printf '%s\n' 'export RUNNERHUB_MCP_PAIR=true'
  if [[ "$URL" == http://* ]]; then
    printf '%s\n' 'export RUNNERHUB_ALLOW_HTTP=true'
    echo "WARNING: raw HTTP is unencrypted; only use it on a trusted private network." >&2
  fi
  printf 'exec %q\n' "$BIN"
} > "$LAUNCHER"
chmod 700 "$LAUNCHER"

codex mcp remove runnerhub >/dev/null 2>&1 || true
codex mcp add runnerhub -- "$LAUNCHER"

echo
echo "RunnerHub MCP installed. Restart Codex, then ask it to call request_access."
echo "Approve the connection at ${URL}/mcp/connections"
echo "Client credentials remain local under ~/.config/runnerhub-mcp/credentials.json"
