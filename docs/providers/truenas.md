# TrueNAS provider

RunnerHub uses the same provider lifecycle on TrueNAS as on generic hosts. The current adapter calls the local `midclt` CLI with structured `app.create`, `app.get_instance`, `app.stop`, and `app.delete` requests. This deliberately keeps TrueNAS-specific behavior out of the scheduler and avoids accepting arbitrary Compose YAML from the RunnerHub UI.

## Storage root is explicit

A TrueNAS installation must set `RUNNERHUB_TRUENAS_ROOT` to a dedicated absolute dataset path, for example:

```bash
export RUNNERHUB_PROVIDER=truenas
export RUNNERHUB_TRUENAS_ROOT=/mnt/POOL/runnerhub
```

RunnerHub deliberately has **no default pool or dataset name**. TrueNAS pool names are installation-specific, so shipping a path such as `/mnt/POOL/...` or `/mnt/Databases/...` as an implicit default would make onboarding appear successful only on one machine layout.

The configured root is used for RunnerHub-managed worker state and short-lived JIT configuration files. Use a dedicated dataset/location that the host-side RunnerHub agent can write and that TrueNAS custom apps can bind-mount.

## Current provider contract

The provider:

- verifies local `midclt` access with `system.version`;
- creates deterministic RunnerHub-owned app names;
- submits a structured custom-app configuration through `app.create`;
- mounts only the worker's protected JIT configuration into the job container;
- uses a tmpfs Actions work directory;
- applies `no-new-privileges` to the job container;
- inspects, stops, and deletes workers by their exact app identity.

The agent using this provider must run in an environment where the local `midclt` command and configured dataset path are actually available. The repository still treats validation against a real target TrueNAS release as an acceptance gate; the presence of this adapter is not evidence that every TrueNAS release or Custom App configuration has been verified.
