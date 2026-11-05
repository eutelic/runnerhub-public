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

## Repository onboarding audit

Below the pool readiness panel, Diagnostics audits repositories visible to the
signed-in identity through the installed GitHub App. It explicitly reports:

- private repositories with no standard `runnerhub` pool;
- repositories with only custom/publisher labels (not silently treated as standard-connected);
- scale-set listeners that are starting, missing, backing off or errored;
- known host-eligibility and capacity blockers;
- configured labels and a link to the repository's GitHub Actions page;
- public repositories as unsupported by the trusted-worker isolation model.

This is a **read-only inventory**. It does not connect repositories, create
trusted pools, inspect GitHub's current queued workflow jobs, or assert that a
particular job requested an available label. A user must explicitly connect
eligible repositories from the dashboard; publisher/fast profiles require
separate, deliberate configuration. An installation may omit repositories, so
the inventory covers only repositories the active GitHub App identity can see.
The repository inventory request may fail separately from the local pool APIs;
the panel shows that limitation instead of presenting a false all-clear.


## GitHub queue snapshot on the Active runners page

The **Active runners** page shows only workers RunnerHub has already created.
It cannot independently report all queued GitHub Actions jobs: GitHub may hold
a job while no RunnerHub worker exists. An empty Active runners table is not
evidence that GitHub has no waiting jobs.

For a read-only per-repository snapshot, click **Check GitHub queues** on
the Active runners page. This inspects only **connected private repositories**
visible to the signed-in GitHub App identity, using the same bounded
`/api/v1/github/repositories/{owner}/{repo}/queued-jobs` endpoint as
Diagnostics. A RunnerHub bearer API token alone is not a GitHub login.

The table reports, separately for each repository:

- observed queued jobs requesting a `runnerhub`-prefixed label;
- distinct workflow runs containing those observed queued jobs;
- observed `waiting` or `pending` jobs on RunnerHub labels;
- other or unknown-label pending jobs (not assumed to require RunnerHub);
- coverage and any per-repository inspection failures.

One scan reads at most two repositories concurrently and runs **only on
request**, not on the ordinary dashboard refresh timer. GitHub may report
additional runs or jobs beyond the bounded inspection; such results show
a **lower-bound** count. Failed inspections show **Unavailable**, never zero.
A completed snapshot retains its timestamp and is marked stale after two
minutes, rather than silently claiming to reflect a live queue.

A count of workflow runs containing queued jobs is **not** a GitHub
queue-position or chronological scheduling estimate. Jobs may be waiting on
environment approvals, dependencies, incompatible labels, eligible hosts,
GitHub capacity, or listener readiness. For a specific job's diagnosis,
use the existing **Inspect pending GitHub jobs** action on Diagnostics.

## Inspect a specific queued GitHub Actions job

From the repository onboarding audit, click **Inspect pending GitHub jobs**
for an installed private repository. RunnerHub performs an authenticated,
read-only inspection of GitHub's latest queued and in-progress workflow
runs. Requests are on-demand, not part of the periodic dashboard refresh.

The inspection reads up to 5 runs in each state and up to 100 jobs per run;
results are marked incomplete when GitHub reports more data than was
retrieved. Each matching pending job shows its GitHub job link, requested
`runs-on` labels and a conservative current-state explanation:

- `not-connected` — no enabled, repository-scoped RunnerHub pool;
- `wrong-label` — pool exists but not for the requested scale-set label;
- `unsupported-label-combination` — requested multiple labels instead of one scale-set label;
- `listener-not-ready` / `listener-failure` / `jit-failure` — known listener/registration problem;
- `no-compatible-host` / `capacity` — host eligibility or occupied concurrency;
- `worker-failure` — worker startup failure reported in health evidence;
- `runtime-unverified` / `unknown` — evidence is not sufficient to diagnose the queue.

These are best-effort classifications from observed state, not declarations
of GitHub's root cause. GitHub may expose job labels only after a workflow
run is planned; jobs without a RunnerHub label are not listed in this panel.
Inspect the linked GitHub job and live scale-set logs when the code is
`unknown`. A queued job is not proof that RunnerHub is broken.

## Bulk onboarding

On the dashboard's **Repositories** page, open **Bulk connect selected
private repositories**. Select the exact repositories to configure. The
preview requires explicit confirmation and only uses existing
repository-scoped connect/profile/publisher endpoints.

The default is standard `runnerhub` pools. Optional checkboxes request
`runnerhub-publisher` (privileged DinD, requires an eligible online host)
or `runnerhub-fast` (restricted to online hosts with `fast=true`).
No public repositories are allowed, missing host classes are skipped, and
existing enabled pools are left unchanged. The sequence stops dependent
operations for a repository if its earlier operation fails; other
repositories can continue.

Successful configuration is reported as **pending listener readiness**, not
as a completed GitHub job. Recheck this Diagnostics page and execute a real
workflow as a separate verification step. Bulk onboarding does not
automatically grant or widen GitHub App repository access.
