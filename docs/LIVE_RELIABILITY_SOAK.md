# Live reliability soak

RunnerHub's normal CI includes simulated lifecycle and Docker/TrueNAS-shaped tests. The live reliability soak is for the separate acceptance gate that exercises real GitHub Actions demand through the deployed RunnerHub scale-set path.

## Run it

Open **Actions → live-reliability-soak → Run workflow**.

The workflow is manual-only. Choose:

- `jobs`: 20, 50, or 100 real GitHub Actions jobs;
- `runner_label`: the RunnerHub label to exercise, normally `runnerhub`;
- `max_parallel`: how much burst pressure GitHub may place on RunnerHub at once;
- `hold_seconds`: how long each assigned runner stays busy.

A 100-job run with `max_parallel=4` is the default acceptance shape. It creates sustained queue pressure without trying to provision 100 workers simultaneously.

## Evidence collected

The final summary records:

- expected vs observed soak jobs;
- successful vs non-success jobs;
- distinct GitHub runner names used;
- p50 and p95 job execution duration;
- whether the optional RunnerHub cleanup verification passed.

Each matrix job also prints its runner name and start/completion timestamps.

## Control-plane cleanup verification

For the strongest acceptance check, configure these repository secrets:

- `RUNNERHUB_SOAK_CONTROL_URL`: URL reachable from a normal `runnerhub` worker;
- `RUNNERHUB_SOAK_ADMIN_KEY`: RunnerHub admin API key.

The workflow never prints the key. After all soak jobs finish, it queries `/api/v1/workers`, filters by the exact GitHub workflow run ID, and waits up to two minutes for those workers to leave active lifecycle states. It fails if workers remain active/stale or any matched worker ends in `failed`.

Without those two secrets, the GitHub-side soak still runs and summarizes successfully, but cleanup verification is explicitly marked skipped.

## What this does not prove

Keep the remaining fault-injection gates separate:

- listener restart while demand is queued;
- cancellation/redelivery behavior;
- runner failure before GitHub assignment;
- host loss during queued/running work;
- heterogeneous 3+ host recovery;
- TrueNAS immutable-image rollback.

Those tests deliberately alter running infrastructure and should not be hidden inside the basic 100-job soak.
