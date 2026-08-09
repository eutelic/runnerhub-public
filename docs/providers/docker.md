# Docker provider

The Docker provider is the MVP execution backend for trusted private repositories.

It uses exact container IDs, an image allowlist, resource limits, a tmpfs work directory, `no-new-privileges`, no published ports, and no Docker socket. JIT configuration is written to a `0600` host file and mounted read-only into the worker, where `run.sh --jitconfig` consumes it.
