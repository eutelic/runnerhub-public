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
            "samples": 1, "eligible": 2, "missing": 1, "coverage_pct": 50.0,
            "p50_ms": 0, "p95_ms": 0
        })
        self.assertIsNone(group["stages"]["jit_ms"]["p95_ms"])

    def test_zero_successes_have_unavailable_coverage(self):
        line = ('{"pool":"p","workload":"dind","cache":"cold",'
                '"success":false,"ready_ms":12}')
        stage = module.summarize([line])[0]["stages"]["ready_ms"]
        group = module.summarize([line])[0]
        self.assertEqual(group["failures"], 1)
        self.assertEqual(group["success_rate_pct"], 0.0)
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

    def test_invalid_samples_fail_closed(self):
        for value in ('-1', '"0"', '1e999'):
            with self.subTest(value=value):
                line = ('{"pool":"p","workload":"dind","cache":"cold",'
                        '"success":true,"jit_ms":%s}' % value)
                with self.assertRaisesRegex(ValueError, "line 1"):
                    module.summarize([line])


if __name__ == "__main__":
    unittest.main()
