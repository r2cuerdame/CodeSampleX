"""Tests for scripts/stats-freshness.py (#517). No network."""

import contextlib
import datetime as dt
import importlib.util
import io
import json
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SPEC = importlib.util.spec_from_file_location("stats_freshness", os.path.join(HERE, "stats-freshness.py"))
freshness = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(freshness)

# Production as diagnosed on 2026-09-26: /v1/stats generatedAt had not moved
# since 2026-09-17T12:34:46Z.
STALL_STAMP = "2026-09-17T12:34:46Z"
DIAGNOSED_AT = dt.datetime(2026, 9, 26, 1, 50, tzinfo=dt.timezone.utc)


def run_cli(*argv):
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        code = freshness.main(list(argv))
    return code, json.loads(out.getvalue())


class FreshnessTest(unittest.TestCase):
    def test_the_production_stall_is_a_violation(self):
        result = freshness.evaluate(STALL_STAMP, DIAGNOSED_AT)
        self.assertFalse(result["ok"])
        self.assertEqual(result["ageSeconds"], 738914)
        self.assertEqual(result["ageDays"], 8.55)

    def test_the_cli_exits_one_on_the_production_stall(self):
        code, result = run_cli("--generated-at", STALL_STAMP, "--now", "2026-09-26T01:50:00Z")
        self.assertEqual(code, 1)
        self.assertFalse(result["ok"])

    def test_a_pass_minutes_old_is_fresh(self):
        code, result = run_cli("--generated-at", "2026-09-26T01:45:00Z", "--now", "2026-09-26T01:50:00Z")
        self.assertEqual(code, 0)
        self.assertTrue(result["ok"])

    def test_the_limit_is_inclusive_at_one_day(self):
        now = dt.datetime(2026, 9, 26, 12, 0, tzinfo=dt.timezone.utc)
        self.assertTrue(freshness.evaluate("2026-09-25T12:00:00Z", now)["ok"])
        self.assertFalse(freshness.evaluate("2026-09-25T11:59:59Z", now)["ok"])

    def test_a_stamp_from_the_future_is_not_fresh(self):
        self.assertFalse(freshness.evaluate("2026-09-27T00:00:00Z", DIAGNOSED_AT)["ok"])

    def test_an_unreadable_stamp_exits_two(self):
        code, result = run_cli("--generated-at", "yesterday", "--now", "2026-09-26T01:50:00Z")
        self.assertEqual(code, 2)
        self.assertFalse(result["ok"])

    def test_offsets_normalize_to_utc(self):
        result = freshness.evaluate("2026-09-26T10:45:00+09:00", DIAGNOSED_AT)
        self.assertEqual(result["generatedAt"], "2026-09-26T01:45:00Z")
        self.assertTrue(result["ok"])


if __name__ == "__main__":
    unittest.main()
