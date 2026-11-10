#!/usr/bin/env python3
"""Summarize measured RunnerHub cold-start stages from JSONL observations.

Input: one JSON object per attempt with pool, workload, cache, success and
optional nonnegative stage durations in milliseconds. Missing != zero.
This is an offline analysis tool; it does not launch workers or alter hosts.
"""
import argparse
import json
import math
import sys
from collections import defaultdict

STAGES = ("queue_to_start_ms", "capacity_wait_ms", "jit_ms", "image_ms", "preflight_ms",
          "provision_ms", "register_ms", "ready_ms", "execution_ms", "cleanup_ms")


def percentile(values, fraction):
    ordered = sorted(values)
    if not ordered:
        return None
    return ordered[math.ceil(fraction * len(ordered)) - 1]


def unique_object_keys(pairs):
    """Reject ambiguous JSON measurements rather than silently using the last key."""
    record = {}
    for key, value in pairs:
        if key in record:
            raise ValueError(f"duplicate key {key}")
        record[key] = value
    return record


def reject_nonfinite_constant(value):
    """Python's JSON decoder accepts NaN/Infinity unless explicitly rejected."""
    raise ValueError(f"non-finite JSON constant {value}")


def summarize(lines):
    groups = defaultdict(list)
    seen_attempts = set()
    for line_number, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            record = json.loads(line, object_pairs_hook=unique_object_keys,
                                parse_constant=reject_nonfinite_constant)
            if not isinstance(record, dict):
                raise ValueError("expected an object")
            for field in ("pool", "workload", "cache"):
                if not isinstance(record.get(field), str) or not record[field].strip() or record[field] != record[field].strip():
                    raise ValueError(f"missing {field}")
            repository = record.get("repository", "unattributed")
            if not isinstance(repository, str) or not repository.strip() or repository != repository.strip():
                raise ValueError("invalid repository")
            # An optional stable attempt ID makes accidental JSONL replays visible.
            # Scope it to the repository so independent repos may reuse IDs.
            attempt_id = record.get("attempt_id")
            if "attempt_id" in record and attempt_id is None:
                raise ValueError("invalid attempt_id")
            if attempt_id is not None:
                if not isinstance(attempt_id, str) or not attempt_id.strip() or attempt_id != attempt_id.strip():
                    raise ValueError("invalid attempt_id")
                attempt_key = (repository, attempt_id)
                if attempt_key in seen_attempts:
                    raise ValueError(f"duplicate attempt_id {attempt_id} for repository {repository}")
            if type(record.get("success")) is not bool:
                raise ValueError("success must be boolean")
            # Reject misspelled duration keys instead of silently losing measurements.
            unknown_stages = sorted(key for key in record if key.endswith("_ms") and key not in STAGES)
            if unknown_stages:
                raise ValueError(f"unknown duration fields: {', '.join(unknown_stages)}")
            for stage in STAGES:
                value = record.get(stage)
                if value is not None and (
                    type(value) not in (int, float)
                    or (type(value) is float and not math.isfinite(value))
                    or value < 0
                    or value > 9007199254740991  # largest exactly representable JSON integer
                ):
                    raise ValueError(f"invalid {stage}")
        except (ValueError, json.JSONDecodeError) as exc:
            raise ValueError(f"line {line_number}: {exc}") from exc
        if attempt_id is not None:
            seen_attempts.add(attempt_key)
        groups[(record.get("repository", "unattributed"),) +
               tuple(record[field] for field in ("pool", "workload", "cache"))].append(record)

    if not groups:
        raise ValueError("no benchmark attempts found")

    output = []
    for key, records in sorted(groups.items()):
        result = dict(zip(("repository", "pool", "workload", "cache"), key))
        result["attempts"] = len(records)
        result["successes"] = sum(r["success"] for r in records)
        result["failures"] = result["attempts"] - result["successes"]
        result["success_rate_pct"] = round(100 * result["successes"] / result["attempts"], 2)
        result["stages"] = {}
        for stage in STAGES:
            values = [r[stage] for r in records if r["success"] and r.get(stage) is not None]
            attempt_values = [r[stage] for r in records if r.get(stage) is not None]
            failed_values = [r[stage] for r in records if not r["success"] and r.get(stage) is not None]
            eligible = result["successes"]
            result["stages"][stage] = {
                "failure_samples": len(failed_values),
                "failure_eligible": result["failures"],
                "failure_missing": result["failures"] - len(failed_values),
                "failure_coverage_pct": round(100 * len(failed_values) / result["failures"], 2) if result["failures"] else None,
                "failure_p50_ms": percentile(failed_values, 0.50),
                "failure_p95_ms": percentile(failed_values, 0.95),
                "attempt_samples": len(attempt_values),
                "attempt_missing": result["attempts"] - len(attempt_values),
                "attempt_coverage_pct": round(100 * len(attempt_values) / result["attempts"], 2),
                "samples": len(values),
                "eligible": eligible,
                "missing": eligible - len(values),
                "coverage_pct": round(100 * len(values) / eligible, 2) if eligible else None,
                "p50_ms": percentile(values, 0.50),
                "p95_ms": percentile(values, 0.95),
            }
        output.append(result)
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("jsonl", help="JSONL measurements or - for stdin")
    parser.add_argument("--min-attempts", type=int, default=1,
                        help="fail if fewer than this many attempts were captured (default: 1)")
    parser.add_argument("--min-cohort-attempts", type=int, default=1,
                        help="require this many attempts in every repository/pool/workload/cache cohort")
    args = parser.parse_args()
    if args.min_attempts < 1:
        parser.error("--min-attempts must be positive")
    if args.min_cohort_attempts < 1:
        parser.error("--min-cohort-attempts must be positive")
    try:
        if args.jsonl == "-":
            report = summarize(sys.stdin)
        else:
            with open(args.jsonl, encoding="utf-8") as source:
                report = summarize(source)
    except (OSError, ValueError) as exc:
        parser.error(str(exc))
    observed = sum(group["attempts"] for group in report)
    if observed < args.min_attempts:
        parser.error(f"only {observed} benchmark attempts; --min-attempts requires {args.min_attempts}")
    for group in report:
        if group["attempts"] < args.min_cohort_attempts:
            label = "/".join(group[field] for field in ("repository", "pool", "workload", "cache"))
            parser.error(f"cohort {label} has only {group['attempts']} attempts; "
                         f"--min-cohort-attempts requires {args.min_cohort_attempts}")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
