# RunnerHub MCP

RunnerHub's MCP is a thin adapter over the existing control plane. It gives an
assistant a compact operational view and a small number of task-oriented
actions; it does not replace RunnerHub's scheduler, policy, or GitHub integration.

## Useful workflows

1. **Diagnose RunnerHub health.** Correlate runtime and pool health, scale-set
   listener status, host availability, active workers, and worker progress.
   Report the underlying evidence and leave the cause unknown when signals do
   not establish one.
2. **Check fleet and job status.** Summarize pools, hosts, and recent worker
   history; filter worker history by state and pool. Follow-up can point users
   to the corresponding GitHub job when one is present in RunnerHub state.
3. **Start one worker.** Request one ephemeral worker in a selected pool. This
   can let an already queued GitHub Actions job proceed. It does not dispatch a
   workflow.

Live GitHub queued-job diagnosis is a valuable next improvement. The current
queued-jobs endpoint relies on a browser GitHub session, so the local MCP does
not claim it can see GitHub's live queue. Add that workflow once a suitable
least-privilege GitHub authorization path is available to MCP clients.

## Tools

| Tool | Access | Purpose |
| --- | --- | --- |
| `runnerhub_overview` | read | Runtime health, fleet versions, pools, hosts, active workers, and scale-set status. |
| `runnerhub_list_pools` | read | Configured pool scopes, labels, isolation, and concurrency. |
| `runnerhub_list_hosts` | read | Host status and capacity. |
| `runnerhub_list_workers` | read | Recent worker history, optionally filtered by state and limit. |
| `runnerhub_get_worker` | read | One worker's lifecycle, GitHub job association, and retained startup diagnostics. |
| `runnerhub_diagnose` | read | Runtime/pool readiness, hosts, workers, progress, and listeners; explicitly notes that it does not inspect GitHub's live queue. |
| `runnerhub_request_worker` | write | Start one worker in an existing pool. Requires operator access and should require confirmation in the MCP client's UI. |

The adapter uses existing RunnerHub API routes, including
`/api/v1/runtime-health`, `/api/v1/pools/{id}/health`,
`/api/v1/scalesets/status`, `/api/v1/workers`, and
`/api/v1/workers/progress`. It does not return the full support bundle by
default because that bundle can contain retained startup diagnostics and audit
details.

## Approval and authentication

The first version runs as a local stdio MCP process. The MCP client presents
the server connection and tool approval UI; the person can accept the
connection and approve or deny individual worker-start calls there. The MCP
client's approval UI is a convenience and is not RunnerHub's authorization
boundary: RunnerHub still checks the request's role, pool security, scheduling,
and policy.

When RunnerHub runs on the same machine, its loopback local-admin mode can
authorize the MCP process without an additional credential. For a remote
RunnerHub, configure `RUNNERHUB_API_TOKEN` with a revocable API key: a viewer
key is sufficient for status and diagnosis, and an operator key is required to
request a worker. The adapter requires HTTPS for remote connections and never
places credentials in tool arguments. Do not give an LLM a raw API key.

Remote Streamable HTTP and browser OAuth consent can be added if hosted MCP
connections become a concrete requirement. That should reuse RunnerHub identity
and issue scoped, revocable tokens rather than adding a separate user database.

## Starting runs

RunnerHub provisions workers for jobs that GitHub Actions has already queued.
`runnerhub_request_worker` starts one ephemeral worker and may let it accept a
queued job; it does not dispatch a workflow. The operation uses the existing
pool's scheduling, concurrency, isolation, and policy checks. It requires the
MCP caller to have RunnerHub operator access.

If GitHub workflow dispatch is added later, expose a separate tool limited to
explicitly configured repositories and workflow IDs, validate inputs
server-side, and require per-call user confirmation. Never accept arbitrary
workflow URLs or shell commands from the model.


## Recommended: dashboard-approved local Codex pairing

This is the same no-tunnel model used by File Ingest. Codex launches a local
RunnerHub stdio MCP process, which connects out to the **existing RunnerHub API**
(on your TrueNAS deployment, usually port 8788). No extra container or inbound
MCP port is required.

1. Redeploy RunnerHub with the pairing-enabled control-plane image.
   The persistent mcp_access.json grant registry is written alongside
   RUNNERHUB_DB (typically /data/mcp_access.json). Keep that directory mounted.
2. On the Linux computer running Codex, with a current checkout of RunnerHub:

   ```bash
   cd ~/repos/runnerhub
   git pull
   bash scripts/install-codex-mcp.sh http://RUNNERHUB_HOST:8788
   ```

   Change the URL if RunnerHub is reachable through another hostname or port.
   The installer builds the existing Go adapter and registers a local stdio
   `runnerhub` MCP in Codex. Go 1.26.11 or compatible toolchain
   auto-download support is required.
3. Restart Codex and ask: **Call request_access on RunnerHub MCP.**
4. Sign into the existing RunnerHub dashboard as an administrator.
   Open **MCP connections**, choose **Approve read-only** (recommended), or
   **Approve operator** to allow explicitly requested worker starts.
5. Ask Codex to call `request_access` again to refresh available
   tools. Restart Codex if its tool-list cache does not refresh.

Before approval, only `request_access` is available. No API key
needs to be copied into Codex. After approval the client stores a generated,
revocable credential in `~/.config/runnerhub-mcp/credentials.json`
(mode 0600). The NAS stores only its SHA-256 digest; an approved grant persists
through reboots until you revoke it from the dashboard.

Viewer scope permits only the MCP's documented fleet/worker read endpoints.
Operator scope also permits a single `POST /api/v1/pools/{id}/workers`
per explicit tool invocation. A paired operator cannot access unrelated API
mutations such as pool editing, host draining, deployer operations or scaling
APIs. Revocation is checked on every RunnerHub API call.

Unapproved public pairing endpoints use bounded inputs, per-IP throttling,
pending expiration after 10 minutes, and explicit administrative approval.
The connections page requires RunnerHub administrator authentication and
same-origin form submissions.

**Network security:** Use private HTTPS whenever possible. The installer
allows plain HTTP only when explicitly opting in to trusted-LAN connections.
Unencrypted HTTP can expose the revocable credential to anyone able to observe
network traffic; never port-forward the RunnerHub dashboard to the Internet.
An existing API token can still be used instead of pairing when required.

## Connect a local MCP client

Run `cmd/runnerhub-mcp` as the MCP client's stdio command. It defaults to
`http://127.0.0.1:8080`; set `RUNNERHUB_URL` for a different control-plane URL.
For a remote control plane, set `RUNNERHUB_API_TOKEN` as an environment secret.
Configure the MCP client to ask before invoking `runnerhub_request_worker`.

For a client that accepts JSON stdio-server configuration, the entry is:

```json
{
  "mcpServers": {
    "runnerhub": {
      "command": "go",
      "args": ["run", "./cmd/runnerhub-mcp"],
      "env": {
        "RUNNERHUB_URL": "https://runnerhub.example.com",
        "RUNNERHUB_API_TOKEN": "use-your-client's-secret-store"
      }
    }
  }
}
```

For local development, omit both `env` values only when RunnerHub is using its
unauthenticated loopback local-admin mode. If `RUNNERHUB_API_KEYS` or browser
authentication is configured on RunnerHub, set `RUNNERHUB_API_TOKEN` to the
appropriate key. For remote deployments, keep the token in the MCP client's
secret store instead of a checked-in config file.
