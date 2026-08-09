# RunnerHub

RunnerHub is a control plane for running GitHub Actions on compute you already own.

The project is deliberately starting with **trusted private-repository workloads** on generic Linux/Docker and TrueNAS hosts. The long-term model is an outbound-only host agent, stable runner pools, ephemeral workers, GitHub App authentication, JIT runner credentials, reconciliation, and stronger VM/microVM isolation for less-trusted workloads.

See [ROADMAP](ROADMAP.md), [security model](docs/SECURITY.md), [release guide](docs/RELEASE.md), and [provider guides](docs/providers/).

## Current status

The repository currently contains the architecture-critical core:

- host and worker lifecycle state models;
- runner-pool and GitHub scope models;
- validation for pool and JIT configuration inputs;
- an infrastructure-neutral `Provider` interface;
- a GitHub runner backend interface that keeps long-lived credentials out of workers;
- a minimal private-by-default control-plane HTTP process.

No production runner provisioning exists yet.

## Development

Requirements:

- Go 1.23+

Run checks:

```bash
go test ./...
go vet ./...
```

Run the control-plane skeleton:

```bash
go run ./cmd/runnerhubd
curl http://127.0.0.1:8080/healthz
```

The HTTP listener intentionally defaults to loopback. Public/LAN binding must be an explicit deployment decision.

## Security boundary

Do not treat container execution as equivalent to VM isolation. Until RunnerHub has a hardened VM/microVM execution path, the container provider is intended only for trusted private-repository CI.

Long-lived GitHub App credentials or PATs must remain in the control plane. Workers should receive only short-lived JIT configuration.
