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

## Explain waiting jobs

The **Diagnostics** page includes a read-only **Why are jobs waiting?** view.
It uses the same authenticated API data already present in the support bundle:

- GitHub App installation configured for each pool;
- scale-set listener state (ready, starting, backing off, error, disabled,
  or not reported);
- eligible hosts and specific host-rejection reasons;
- active/busy workers relative to the configured per-pool concurrency.

The page differentiates *reported facts* from possible bottlenecks. For
example, "concurrency is occupied" means a new job may wait, not that
GitHub has definitely queued a job. A listener's last reported timestamp is
not itself a liveness proof; do not treat an old timestamp as definitive
outage evidence.

Use **Refresh bundle** to refresh readiness and the full incident bundle.
No GitHub API tokens, incoming events, or runner settings are changed by
this panel.
