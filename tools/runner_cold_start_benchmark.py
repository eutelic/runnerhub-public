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

STAGES = ("jit_ms", "image_ms", "preflight_ms", "provision_ms", "register_ms", "ready_ms")


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
    for line_number, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            record = json.loads(line, object_pairs_hook=unique_object_keys,
                                parse_constant=reject_nonfinite_constant)
            if not isinstance(record, dict):
                raise ValueError("expected an object")
            for field in ("pool", "workload", "cache"):
                if not isinstance(record.get(field), str) or not record[field]:
                    raise ValueError(f"missing {field}")
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
        groups[tuple(record[field] for field in ("pool", "workload", "cache"))].append(record)

    output = []
    for key, records in sorted(groups.items()):
        result = dict(zip(("pool", "workload", "cache"), key))
        result["attempts"] = len(records)
        result["successes"] = sum(r["success"] for r in records)
        result["failures"] = result["attempts"] - result["successes"]
        result["success_rate_pct"] = round(100 * result["successes"] / result["attempts"], 2)
        result["stages"] = {}
        for stage in STAGES:
            values = [r[stage] for r in records if r["success"] and r.get(stage) is not None]
            eligible = result["successes"]
            result["stages"][stage] = {
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
    args = parser.parse_args()
    try:
        if args.jsonl == "-":
            report = summarize(sys.stdin)
        else:
            with open(args.jsonl, encoding="utf-8") as source:
                report = summarize(source)
    except (OSError, ValueError) as exc:
        parser.error(str(exc))
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
