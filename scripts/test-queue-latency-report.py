#!/usr/bin/env python3
"""Regression coverage for read-only queue latency reporting."""
import importlib.util
import pathlib
import unittest

path = pathlib.Path(__file__).with_name("queue-latency-report.py")
spec = importlib.util.spec_from_file_location("queue_latency_report", path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class QueueLatencyTests(unittest.TestCase):
    def test_missing_is_not_zero_and_measured_zero_is_valid(self):
        rows = [
            {"poolId": "standard", "repository": "a/b",
             "queuedAt": "2026-11-08T12:00:00Z", "startedAt": "2026-11-08T12:00:00Z"},
            {"poolId": "standard", "repository": "a/b", "createdAt": "2026-11-08T12:00:00Z",
             "startedAt": "2026-11-08T12:00:10Z"},
        ]
        report = module.summarize(rows)[0]
        self.assertEqual((report["workers"], report["measured"], report["unavailable"]), (2, 1, 1))
        self.assertEqual(report["queueToStartSeconds"], {"p50": 0, "p95": 0})
        self.assertEqual(report["coverage"], 0.5)

    def test_distinct_pools_and_invalid_order(self):
        rows = [
            {"poolId": "standard", "repository": "a/b", "queuedAt": "2026-11-08T12:00:00Z",
             "startedAt": "2026-11-08T12:00:10Z"},
            {"poolId": "publisher", "repository": "a/b", "queuedAt": "2026-11-08T12:00:00Z",
             "startedAt": "2026-11-08T12:02:00Z"},
            {"poolId": "publisher", "repository": "a/b", "queuedAt": "2026-11-08T12:02:00Z",
             "startedAt": "2026-11-08T12:00:00Z"},
        ]
        reports = {r["poolId"]: r for r in module.summarize(rows)}
        self.assertEqual(reports["standard"]["queueToStartSeconds"]["p95"], 10)
        self.assertEqual(reports["publisher"]["queueToStartSeconds"]["p95"], 120)
        self.assertEqual(reports["publisher"]["invalidOrder"], 1)
        self.assertEqual(reports["publisher"]["unavailable"], 1)

    def test_no_data_and_naive_timestamps(self):
        self.assertEqual(module.summarize([]), [])
        report = module.summarize([{"queuedAt": "2026-11-08T12:00:00",
                                    "startedAt": "2026-11-08T12:00:00Z"}])[0]
        self.assertIsNone(report["queueToStartSeconds"]["p95"])
        self.assertEqual(report["coverage"], 0)


if __name__ == "__main__":
    unittest.main()
