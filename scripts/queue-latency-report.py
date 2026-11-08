#!/usr/bin/env python3
"""Read-only, coverage-aware queue latency report from exported RunnerHub workers.

Usage: python3 scripts/queue-latency-report.py workers.json
Accepts a JSON array or {"workers": [...]} exported from the control plane.
Never substitutes worker creation time for missing GitHub queuedAt.
"""
import argparse
import datetime as dt
import json
import math
import sys
from collections import defaultdict


def timestamp(value):
    if not isinstance(value, str) or not value:
        return None
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
        if parsed.tzinfo is None:
            return None
        return parsed.astimezone(dt.timezone.utc)
    except ValueError:
        return None


def percentile(values, p):
    ordered = sorted(values)
    position = (len(ordered) - 1) * p
    low = math.floor(position)
    high = math.ceil(position)
    return round(ordered[low] + (ordered[high] - ordered[low]) * (position - low), 3)


def summarize(workers):
    groups = defaultdict(list)
    for worker in workers:
        if not isinstance(worker, dict):
            raise ValueError("worker records must be objects")
        # Repository can be absent for pre-assignment or warm workers.
        repo = worker.get("repository") or "(unassigned)"
        pool = worker.get("poolId") or "(unknown)"
        groups[(str(pool), str(repo))].append(worker)
    result = []
    for (pool, repo), records in sorted(groups.items()):
        durations = []
        invalid = 0
        for worker in records:
            queued = timestamp(worker.get("queuedAt"))
            started = timestamp(worker.get("startedAt"))
            if queued is None or started is None:
                continue
            elapsed = (started - queued).total_seconds()
            if elapsed < 0:
                invalid += 1
                continue
            durations.append(elapsed)
        result.append({
            "poolId": pool,
            "repository": repo,
            "workers": len(records),
            "measured": len(durations),
            "unavailable": len(records) - len(durations),
            "invalidOrder": invalid,
            "coverage": round(len(durations) / len(records), 4),
            "queueToStartSeconds": {
                "p50": percentile(durations, 0.5) if durations else None,
                "p95": percentile(durations, 0.95) if durations else None,
            },
        })
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", help="local JSON worker export (never uploaded)")
    args = parser.parse_args()
    with open(args.input, encoding="utf-8") as source:
        data = json.load(source)
    workers = data.get("workers") if isinstance(data, dict) else data
    if not isinstance(workers, list):
        parser.error("expected a JSON worker array or object with workers array")
    try:
        report = summarize(workers)
    except ValueError as exc:
        parser.error(str(exc))
    json.dump({"groups": report, "totalWorkers": len(workers)}, sys.stdout, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
