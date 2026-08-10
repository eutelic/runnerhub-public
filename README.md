# RunnerHub

RunnerHub is a control plane for running GitHub Actions on compute you already own: Linux servers, NAS boxes, workstations, VMs and specialized hardware.

The default target is **trusted private-repository CI** with ephemeral workers. RunnerHub keeps long-lived GitHub credentials in trusted control components, hands workers only short-lived JIT configuration, schedules pools across hosts, reconciles provider and GitHub state independently, and removes workers by exact identity.

**Start with the [alpha quickstart](docs/QUICKSTART.md)** for the complete GitHub App → Docker host → automatic first-job path.

See [ROADMAP](ROADMAP.md), [security model](docs/SECURITY.md), [release guide](docs/RELEASE.md), and [provider guides](docs/providers/).

## Implemented

- GitHub App + direct JIT runner backend and repository/org discovery
- outbound GitHub Runner Scale Set listener for automatic demand-driven workers
- GitHub runner-group security posture checks against actual repository/workflow restrictions
- SQLite state, audit, reconciliation and a durable leased command queue with encrypted JIT payloads
- bootstrap enrollment followed by persisted, rotating per-host credentials
- Docker and TrueNAS providers
- multi-host resource/capability scheduler, global pool concurrency and draining
- dashboard/API, API-key roles, a constrained scale-set API role, and trusted reverse-proxy identity-header integration
- scale-set restart recovery, per-demand active-worker resync and job/repository/workflow timing metadata
- experimental Firecracker and Proxmox providers
- trusted process provider for macOS/Windows/special hardware
- signed atomic update staging, image/version inventory, local CAS cache and cost/capacity telemetry
- 100-job simulated lifecycle soak exercising scheduler/store/agent/reconciler with zero simulated stale registrations
- CI with tests, race detection, vet, `govulncheck`, macOS/Windows agent cross-builds and a full Docker Compose startup/enrollment/security smoke test

## Fastest local Docker install

The default single-host setup keeps the control API on loopback and uses an outbound scale-set listener for automatic job demand:

```bash
bash scripts/init-docker-env.sh
# Fill in the four GitHub values in deploy/docker/.env.
bash scripts/start-docker.sh
```

The initializer generates independent enrollment, agent, admin and constrained scale-set credentials and writes the local `.env` with mode `0600`. The start script builds the control plane and Docker agent, waits for enrollment, creates a repository-scoped pool, starts the scale-set listener, and does not report success until GitHub authentication/message-session setup completes. Use the configured scale-set name (default `runnerhub`) directly in `runs-on`.

No inbound GitHub webhook or public RunnerHub listener is required for this default path. Ephemeral Docker workers refresh the configured runner image by default so a locally cached moving tag does not silently age out. The `.env`, private keys, and RunnerHub database are excluded by `.gitignore`. See the [alpha quickstart](docs/QUICKSTART.md) for GitHub App permissions and the first workflow.

## Development

CI and production container builds use **Go 1.26**. The core module remains compatible with its declared Go version, while the `scaleset/` module is tested separately against GitHub's standalone scale-set client.

```bash
go test ./...
go test -race ./internal/...
go test -tags localtest ./tests/soak -count=1
go vet ./...
```

The normal SQLite implementation uses the CGO-free `modernc.org/sqlite` driver. `localtest` exists only to let the lifecycle soak run in restricted/offline development environments.

## Run the control plane from source

```bash
export RUNNERHUB_ENROLLMENT_TOKEN='replace-with-a-long-random-value'
export RUNNERHUB_AGENT_SECRET='replace-with-at-least-32-random-bytes'
export RUNNERHUB_API_KEYS='replace-with-a-long-random-key:admin:local-admin'
go run ./cmd/runnerhubd
```

The listener defaults to `127.0.0.1:8080`. For a non-loopback listener, configure `RUNNERHUB_TLS_CERT_FILE` and `RUNNERHUB_TLS_KEY_FILE`, or deliberately set `RUNNERHUB_ALLOW_INSECURE_HTTP=true` only when another private network layer already protects the connection.

## Enroll a Docker host agent from source

First launch:

```bash
export RUNNERHUB_CONTROL_PLANE_URL='https://runnerhub.example.com'
export RUNNERHUB_ENROLLMENT_TOKEN='same-enrollment-token'
export RUNNERHUB_PROVIDER='docker'
go run ./cmd/runnerhub-agent
```

After successful enrollment, the agent writes only its rotating per-host credential to `RUNNERHUB_CREDENTIAL_FILE` (default under `RUNNERHUB_STATE_DIR`). The bootstrap enrollment token can then be removed from that host's environment. If the stored credential is later rejected after a long offline period, supply the enrollment token again to re-enroll deliberately.

No production runner ports are published by default and the Docker socket is not mounted into job containers.
