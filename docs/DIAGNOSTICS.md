# RunnerHub diagnostics

RunnerHub can retain bounded, redacted startup evidence for Docker workers that exit before GitHub reaches a runnable/job-started state.

## Enable startup diagnostics

Set this on the RunnerHub **agent** service:

```yaml
RUNNERHUB_DIAGNOSTICS_ENABLED: "true"
```

The TrueNAS template exposes the same value through:

```yaml
x-runnerhub-config:
  diagnostics-enabled: &diagnostics-enabled "true"
```

When enabled, clean Docker exits that the provider reports as completed can be supplemented with the last 200 container log lines. The control plane only persists those diagnostics while the worker is still pre-job.

Diagnostics are bounded and redact common authorization headers, GitHub tokens, JWTs, token/secret/password/JIT-style assignments, and long opaque token-like strings. Collection stops when markers associated with normal job execution appear so workflow output is not intentionally retained.

## Copy a support bundle

Open:

```text
/diagnostics.html
```

or use the **Diagnostics** button on the dashboard.

The page gathers the authenticated RunnerHub API state needed for troubleshooting:

- hosts;
- pools;
- up to 100 most recent workers, including retained startup diagnostics;
- 200 recent audit events;
- scale-set listener status;
- runtime health;
- fleet/image versions;
- metrics.

Use **Copy bundle** to copy the JSON into an issue or diagnostic session. Review the bundle before sharing it outside the trusted RunnerHub environment.
