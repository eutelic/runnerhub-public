"""Offline tests for cold-start benchmark aggregation."""
import importlib.util
import io
import pathlib
import unittest

SCRIPT = pathlib.Path(__file__).with_name("runner_cold_start_benchmark.py")
spec = importlib.util.spec_from_file_location("cold_start_benchmark", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class BenchmarkTests(unittest.TestCase):
    def test_missing_is_not_zero_and_failed_attempts_excluded(self):
        lines = io.StringIO(
            '{"pool":"p","workload":"dind","cache":"warm","success":true,"ready_ms":0}\n'
            '{"pool":"p","workload":"dind","cache":"warm","success":true}\n'
            '{"pool":"p","workload":"dind","cache":"warm","success":false,"ready_ms":999}\n'
        )
        group = module.summarize(lines)[0]
        self.assertEqual((group["attempts"], group["successes"]), (3, 2))
        self.assertEqual(group["failures"], 1)
        self.assertEqual(group["success_rate_pct"], 66.67)
        self.assertEqual(group["stages"]["ready_ms"], {
            "failure_samples": 1, "failure_eligible": 1, "failure_missing": 0,
            "failure_coverage_pct": 100.0, "failure_p50_ms": 999, "failure_p95_ms": 999,
            "attempt_samples": 2, "attempt_missing": 1, "attempt_coverage_pct": 66.67,
            "samples": 1, "eligible": 2, "missing": 1, "coverage_pct": 50.0,
            "p50_ms": 0, "p95_ms": 0
        })
        self.assertIsNone(group["stages"]["jit_ms"]["p95_ms"])
        # Failed attempts retain measured durations for coverage accounting,
        # without contaminating successful-run latency percentiles.
        self.assertEqual(group["stages"]["ready_ms"]["attempt_samples"], 2)
        self.assertEqual(group["stages"]["ready_ms"]["p95_ms"], 0)

    def test_zero_successes_have_unavailable_coverage(self):
        line = ('{"pool":"p","workload":"dind","cache":"cold",'
                '"success":false,"ready_ms":12}')
        stage = module.summarize([line])[0]["stages"]["ready_ms"]
        group = module.summarize([line])[0]
        self.assertEqual(group["failures"], 1)
        self.assertEqual(group["success_rate_pct"], 0.0)
        self.assertEqual(stage["failure_samples"], 1)
        self.assertEqual(stage["failure_eligible"], 1)
        self.assertEqual(stage["failure_missing"], 0)
        self.assertEqual(stage["failure_coverage_pct"], 100.0)
        self.assertEqual(stage["failure_p50_ms"], 12)
        self.assertEqual(stage["failure_p95_ms"], 12)
        self.assertEqual(stage["attempt_samples"], 1)
        self.assertEqual(stage["attempt_coverage_pct"], 100.0)
        self.assertEqual(stage["samples"], 0)
        self.assertEqual(stage["eligible"], 0)
        self.assertEqual(stage["missing"], 0)
        self.assertIsNone(stage["coverage_pct"])
        self.assertIsNone(stage["p95_ms"])

    def test_separate_cache_groups_and_nearest_rank(self):
        def row(cache, duration):
            return ('{"pool":"p","workload":"standard","cache":"%s",'
                    '"success":true,"ready_ms":%d}\n' % (cache, duration))
        groups = module.summarize(io.StringIO(
            "".join(row("cold", i) for i in range(1, 21)) + row("warm", 1)
        ))
        self.assertEqual(groups[0]["stages"]["ready_ms"]["p95_ms"], 19)
        self.assertEqual(groups[1]["stages"]["ready_ms"]["p95_ms"], 1)

    def test_queue_phase_coverage_by_repository_and_pool(self):
        lines = io.StringIO(
            '{"repository":"org/one","pool":"standard","workload":"ci","cache":"warm",'
            '"success":true,"queue_to_start_ms":0,"capacity_wait_ms":10,"cleanup_ms":7}\n'
            '{"repository":"org/one","pool":"standard","workload":"ci","cache":"warm",'
            '"success":true,"capacity_wait_ms":20}\n'
            '{"repository":"org/two","pool":"publisher","workload":"ci","cache":"warm",'
            '"success":true,"queue_to_start_ms":300}\n'
        )
        groups = module.summarize(lines)
        self.assertEqual(len(groups), 2)
        standard = next(g for g in groups if g["pool"] == "standard")
        publisher = next(g for g in groups if g["pool"] == "publisher")
        self.assertIsNone(standard["stages"]["queue_to_start_ms"]["failure_coverage_pct"])
        self.assertIsNone(standard["stages"]["queue_to_start_ms"]["failure_p95_ms"])
        self.assertEqual(standard["repository"], "org/one")
        self.assertEqual(publisher["repository"], "org/two")
        self.assertEqual(standard["stages"]["queue_to_start_ms"]["samples"], 1)
        self.assertEqual(standard["stages"]["queue_to_start_ms"]["missing"], 1)
        self.assertEqual(standard["stages"]["queue_to_start_ms"]["p50_ms"], 0)
        self.assertEqual(standard["stages"]["capacity_wait_ms"]["p95_ms"], 20)
        self.assertEqual(publisher["stages"]["cleanup_ms"]["coverage_pct"], 0.0)
        self.assertIsNone(publisher["stages"]["cleanup_ms"]["p95_ms"])

    def test_group_labels_reject_whitespace_padding(self):
        # Invisible padding must not split one pool/repository into multiple
        # apparent latency cohorts or silently create blank cohort labels.
        for field in ("pool", "workload", "cache", "repository"):
            for value in (" ", " value", "value ", "\tvalue"):
                with self.subTest(field=field, value=value):
                    record = {"pool": "p", "workload": "ci", "cache": "warm",
                              "repository": "org/repo", "success": True}
                    record[field] = value
                    import json
                    with self.assertRaisesRegex(ValueError, "line 1:"):
                        module.summarize([json.dumps(record)])

    def test_duplicate_attempt_ids_rejected_within_repository(self):
        import json
        base = {"repository": "org/one", "pool": "p", "workload": "ci",
                "cache": "warm", "success": True, "attempt_id": "run-123"}
        with self.assertRaisesRegex(ValueError, "line 2: duplicate attempt_id"):
            module.summarize([json.dumps(base), json.dumps(base)])
        other = dict(base, repository="org/two")
        self.assertEqual(len(module.summarize([json.dumps(base), json.dumps(other)])), 2)

    def test_invalid_attempt_id_rejected(self):
        import json
        for value in ("", " ", " padded", "padded ", 123, False, None):
            with self.subTest(value=value):
                row = {"pool": "p", "workload": "ci", "cache": "warm",
                       "success": True, "attempt_id": value}
                with self.assertRaisesRegex(ValueError, "line 1: invalid attempt_id"):
                    module.summarize([json.dumps(row)])

    def test_empty_benchmark_is_not_a_successful_report(self):
        # A missing or empty capture must not masquerade as a valid zero-job
        # benchmark during acceptance or release comparisons.
        for lines in ([], ["", "  ", "\n"]):
            with self.subTest(lines=lines):
                with self.assertRaisesRegex(ValueError, "no benchmark attempts found"):
                    module.summarize(lines)

    def test_blank_repository_rejected(self):
        line = ('{"repository":" ","pool":"p","workload":"ci","cache":"warm",'
                '"success":true}')
        with self.assertRaisesRegex(ValueError, "line 1: invalid repository"):
            module.summarize([line])

    def test_duplicate_json_keys_rejected(self):
        # JSON parsers otherwise silently retain the last value, corrupting
        # latency groups and hiding contradictory success observations.
        for duplicate in ('"success":false', '"ready_ms":999', '"pool":"other"'):
            with self.subTest(duplicate=duplicate):
                line = ('{"pool":"p","workload":"dind","cache":"cold",'
                        '"success":true,"ready_ms":1,' + duplicate + '}')
                with self.assertRaisesRegex(ValueError, "line 1: duplicate key"):
                    module.summarize([line])

    def test_nonstandard_nonfinite_json_rejected_even_in_metadata(self):
        # Python's decoder accepts these non-JSON tokens by default. Reject
        # them even when nested or not part of a measured stage.
        for value in ("NaN", "Infinity", "-Infinity"):
            with self.subTest(value=value):
                line = ('{"pool":"p","workload":"dind","cache":"cold",'
                        '"success":true,"metadata":{"raw":' + value + '}}')
                with self.assertRaisesRegex(ValueError, "line 1: non-finite JSON constant"):
                    module.summarize([line])

    def test_misspelled_duration_field_rejected(self):
        line = ('{"pool":"p","workload":"dind","cache":"cold",'
                '"success":true,"registerd_ms":100}')
        with self.assertRaisesRegex(ValueError, "line 1: unknown duration fields: registerd_ms"):
            module.summarize([line])

    def test_oversized_integer_duration_has_line_scoped_error(self):
        # math.isfinite(10**400) raises OverflowError rather than ValueError.
        # Large JSON integers must not crash the reporting CLI.
        for value in ("9007199254740992", "1" + "0" * 400):
            with self.subTest(value=value[:20]):
                line = ('{"pool":"p","workload":"dind","cache":"cold",'
                        '"success":true,"jit_ms":' + value + '}')
                with self.assertRaisesRegex(ValueError, "line 1: invalid jit_ms"):
                    module.summarize([line])

    def test_min_attempts_cli_acceptance_gate(self):
        # A benchmark with fewer than the requested 20/50/100 observations
        # must fail rather than publishing a misleading acceptance report.
        import contextlib
        import json
        from unittest import mock

        row = json.dumps({"pool": "p", "workload": "ci", "cache": "warm",
                          "success": True})
        for minimum, count, should_pass in ((2, 1, False), (2, 2, True),
                                             (20, 2, False)):
            with self.subTest(minimum=minimum, count=count):
                stdout, stderr = io.StringIO(), io.StringIO()
                with mock.patch("sys.argv", ["benchmark", "-", "--min-attempts",
                                             str(minimum)]), \
                     mock.patch("sys.stdin", io.StringIO((row + "\n") * count)), \
                     contextlib.redirect_stdout(stdout), \
                     contextlib.redirect_stderr(stderr):
                    if should_pass:
                        module.main()
                        self.assertEqual(sum(g["attempts"] for g in
                                             json.loads(stdout.getvalue())), count)
                    else:
                        with self.assertRaises(SystemExit) as error:
                            module.main()
                        self.assertEqual(error.exception.code, 2)
                        self.assertIn("--min-attempts requires", stderr.getvalue())

    def test_min_attempts_must_be_positive(self):
        import contextlib
        from unittest import mock
        stderr = io.StringIO()
        with mock.patch("sys.argv", ["benchmark", "-", "--min-attempts", "0"]), \
             contextlib.redirect_stderr(stderr):
            with self.assertRaises(SystemExit) as error:
                module.main()
        self.assertEqual(error.exception.code, 2)
        self.assertIn("--min-attempts must be positive", stderr.getvalue())

    def test_min_cohort_attempts_prevents_underpowered_cohorts(self):
        import contextlib
        import json
        from unittest import mock

        def row(pool):
            return json.dumps({"repository": "org/repo", "pool": pool,
                               "workload": "ci", "cache": "warm", "success": True}) + "\n"

        for minimum, data, should_pass in (
            (2, row("a") * 2 + row("b"), False),
            (2, row("a") * 2 + row("b") * 2, True),
        ):
            with self.subTest(minimum=minimum, should_pass=should_pass):
                stderr, stdout = io.StringIO(), io.StringIO()
                with mock.patch("sys.argv", ["benchmark", "-", "--min-cohort-attempts",
                                             str(minimum)]), \
                     mock.patch("sys.stdin", io.StringIO(data)), \
                     contextlib.redirect_stderr(stderr), \
                     contextlib.redirect_stdout(stdout):
                    if should_pass:
                        module.main()
                        self.assertEqual(len(json.loads(stdout.getvalue())), 2)
                    else:
                        with self.assertRaises(SystemExit) as error:
                            module.main()
                        self.assertEqual(error.exception.code, 2)
                        self.assertIn("cohort org/repo/b/ci/warm", stderr.getvalue())

    def test_min_cohort_attempts_rejects_zero(self):
        import contextlib
        from unittest import mock
        stderr = io.StringIO()
        with mock.patch("sys.argv", ["benchmark", "-", "--min-cohort-attempts", "0"]), \
             contextlib.redirect_stderr(stderr):
            with self.assertRaises(SystemExit) as error:
                module.main()
        self.assertEqual(error.exception.code, 2)
        self.assertIn("--min-cohort-attempts must be positive", stderr.getvalue())

    def test_invalid_samples_fail_closed(self):
        for value in ('-1', '"0"', '1e999'):
            with self.subTest(value=value):
                line = ('{"pool":"p","workload":"dind","cache":"cold",'
                        '"success":true,"jit_ms":%s}' % value)
                with self.assertRaisesRegex(ValueError, "line 1"):
                    module.summarize([line])


if __name__ == "__main__":
    unittest.main()
