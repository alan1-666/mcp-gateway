import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("go_coverage", Path(__file__).parents[1] / "check-go-coverage.py")
coverage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(coverage)


class CoverageGateTests(unittest.TestCase):
    def test_low_missing_and_empty_fail(self):
        profile = "mode: atomic\nmodule/a.go:1.1,2.1 9 1\nmodule/a.go:3.1,4.1 1 0\nmodule/empty.go:1.1,1.2 0 1\n"
        self.assertEqual(coverage.check(profile, {"a.go": 90})[1], [])
        self.assertEqual(len(coverage.check(profile, {"a.go": 91, "missing.go": 90, "empty.go": 90})[1]), 3)

    def test_duplicate_instrumentation_is_counted_once(self):
        reports, errors = coverage.check("mode: count\nm/a.go:1.1,2.1 9 0\nm/a.go:1.1,2.1 9 2\nm/a.go:3.1,4.1 1 0", {"a.go": 90})
        self.assertFalse(errors)
        self.assertEqual(reports[0]["statements"], 10)
        self.assertEqual(reports[0]["percent"], 90)

    def test_invalid_and_ambiguous_profiles_fail(self):
        for profile in ["", "mode: invalid", "mode: set\nm/a.go:1 1 -1", "mode: set\nm/a.go:1 1 1\nm/a.go:1 2 1"]:
            with self.assertRaises(ValueError):
                coverage.coverage_by_file(profile)
        self.assertTrue(coverage.check("mode: set\none/a.go:1 1 1\ntwo/a.go:1 1 1", {"a.go": 90})[1])
