# RunnerHub

RunnerHub is a control plane for running GitHub Actions on compute you already own: Linux servers, NAS boxes, workstations, VMs and specialized hardware.

The default target is **trusted private-repository CI** with ephemeral workers. RunnerHub keeps long-lived GitHub credentials in the control plane, hands workers only short-lived JIT configuration, schedules pools across hosts, reconciles provider and GitHub state independently, and removes workers by exact identity.

See [ROADMAP](ROADMAP.md), [security model](docs/SECURITY.md), [release guide](docs/RELEASE.md), and [provider guides](docs/providers/).

## Implemented

- GitHub App + direct JIT runner backend and repository/org discovery
- SQLite state, audit, reconciliation and 100-job simulated soak
- outbound host agent with per-host authentication
- Docker and TrueNAS providers
- multi-host resource/capability scheduler and draining
- dashboard/API, RBAC-style API keys and OIDC-header integration
- isolated GitHub Runner Scale Set adapter
- experimental Firecracker and Proxmox providers
- trusted process provider for macOS/Windows/special hardware
- signed update staging, image/version inventory, local CAS cache and cost/capacity telemetry

## Development

Core requires Go 1.23+. The separate `scaleset/` module follows GitHub's current client and requires Go 1.25+.

```bash
go test ./...
go vet ./...
```

The normal SQLite implementation uses the CGO-free `modernc.org/sqlite` driver. `localtest` exists only to let the lifecycle suite run in restricted/offline development environments.

## Run the control plane

```bash
export RUNNERHUB_ENROLLMENT_TOKEN='replace-with-a-long-random-value'
export RUNNERHUB_AGENT_SECRET='replace-with-at-least-32-random-bytes'
export RUNNERHUB_API_KEYS='replace-me:admin:local-admin'
go run ./cmd/runnerhubd
```

The HTTP listener defaults to `127.0.0.1:8080`. Put it behind TLS/Tailscale or another authenticated private ingress before remote use.

## Run a Docker host agent

```bash
export RUNNERHUB_CONTROL_PLANE_URL='http://127.0.0.1:8080'
export RUNNERHUB_ENROLLMENT_TOKEN='same-enrollment-token'
export RUNNERHUB_PROVIDER='docker'
go run ./cmd/runnerhub-agent
```

No production runner ports are published by default and the Docker socket is not mounted into job containers.
