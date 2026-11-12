import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('complexity', Path(__file__).with_name('check-go-complexity.py'))
complexity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(complexity)


class ComplexityGateTests(unittest.TestCase):
    def test_new_functions_have_ceiling(self):
        self.assertEqual(complexity.check(['30 pkg New internal/x.go:1:1'], {}), [])
        self.assertTrue(complexity.check(['31 pkg New internal/x.go:1:1'], {}))

    def test_legacy_debt_cannot_grow(self):
        baseline = {'pkg (*Type).Old internal/x.go': 45}
        for score in (20, 44, 45):
            self.assertEqual(complexity.check([f'{score} pkg (*Type).Old internal/x.go:20:1'], baseline), [])
        self.assertTrue(complexity.check(['46 pkg (*Type).Old internal/x.go:1:1'], baseline))
        self.assertTrue(complexity.check(['45 pkg (*Type).Other internal/x.go:1:1'], baseline))

    def test_missing_or_malformed_results_fail_closed(self):
        for lines in ([], [''], ['not scanner output'], ['30 pkg New x.go:bad:1']):
            with self.assertRaises(ValueError):
                complexity.check(lines, {})
