import contextlib
import datetime as dt
import importlib.util
import io
import json
import os
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SPEC = importlib.util.spec_from_file_location("kpi_world_state", os.path.join(HERE, "kpi-world-state.py"))
world = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(world)

BEFORE = dt.datetime(2026, 11, 17, 3, 0, tzinfo=dt.timezone.utc)
JUDGED = dt.datetime(2026, 11, 24, 0, 0, tzinfo=dt.timezone.utc)


def stats(seven, generated="2026-11-17T02:30:00Z", **extra):
    doc = {"generatedAt": generated, "activeInstallations1d": 1, "activeInstallations7d": seven,
           "activeInstallations30d": 9}
    doc.update(extra)
    return doc


def run_cli(*argv):
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        code = world.main(list(argv))
    return code, json.loads(out.getvalue())


class WorldStateTest(unittest.TestCase):
    def test_below_target_before_judgment_is_pending(self):
        result = world.evaluate(stats(3), BEFORE)
        self.assertTrue(result["fresh"])
        self.assertEqual(result["pulse"]["activeInstalls7d"], 3)
        self.assertEqual(result["pulse"]["measuredAt"], "2026-11-17T02:30:00Z")
        self.assertEqual(result["pulse"]["source"], "measured")
        self.assertEqual(result["kpi"]["gap"], 7)
        self.assertEqual(result["kpi"]["daysLeft"], 7)
        self.assertEqual(result["kpi"]["verdict"], "pending")

    def test_judgment_instant_is_inclusive(self):
        self.assertEqual(world.evaluate(stats(9, "2026-11-23T23:00:00Z"), JUDGED)["kpi"]["verdict"], "missed")
        self.assertEqual(world.evaluate(stats(10, "2026-11-23T23:00:00Z"), JUDGED)["kpi"]["verdict"], "achieved")
        early = JUDGED - dt.timedelta(seconds=1)
        result = world.evaluate(stats(9, "2026-11-23T23:00:00Z"), early)
        self.assertEqual(result["kpi"]["verdict"], "pending")
        self.assertEqual(result["kpi"]["daysLeft"], 1)

    def test_reaching_target_early_is_achieved(self):
        result = world.evaluate(stats(12), BEFORE)
        self.assertEqual(result["kpi"]["verdict"], "achieved")
        self.assertEqual(result["kpi"]["gap"], 0)

    def test_installations_never_fill_people_counts(self):
        pulse = world.evaluate(stats(3), BEFORE)["pulse"]
        self.assertNotIn("dau", pulse)
        self.assertNotIn("mau", pulse)
        self.assertNotIn("revenueUsd", pulse)

    def test_replan_when_node_misses_expectation(self):
        self.assertTrue(world.evaluate(stats(3), BEFORE, expected=5)["replan"]["required"])
        self.assertFalse(world.evaluate(stats(5), BEFORE, expected=5)["replan"]["required"])
        self.assertNotIn("replan", world.evaluate(stats(3), BEFORE))

    def test_stale_stats_exit_one(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "stats.json")
            with open(path, "w", encoding="utf-8") as handle:
                json.dump(stats(3, "2026-11-15T00:00:00Z"), handle)
            code, result = run_cli("--stats-file", path, "--now", "2026-11-17T03:00:00Z")
        self.assertEqual(code, 1)
        self.assertFalse(result["fresh"])
        self.assertEqual(result["kpi"]["current"], 3)

    def test_fresh_stats_exit_zero(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "stats.json")
            with open(path, "w", encoding="utf-8") as handle:
                json.dump(stats(4), handle)
            code, result = run_cli("--stats-file", path, "--now", "2026-11-17T03:00:00Z", "--expected", "4")
        self.assertEqual(code, 0)
        self.assertFalse(result["replan"]["required"])

    def test_missing_metric_is_unknown_not_zero(self):
        for broken in ({"generatedAt": "2026-11-17T02:30:00Z"}, stats(-1), stats(True), stats("3"), []):
            with tempfile.TemporaryDirectory() as tmp:
                path = os.path.join(tmp, "stats.json")
                with open(path, "w", encoding="utf-8") as handle:
                    json.dump(broken, handle)
                code, result = run_cli("--stats-file", path, "--now", "2026-11-17T03:00:00Z")
            self.assertEqual(code, 2)
            self.assertIsNone(result["pulse"]["activeInstalls7d"])
            self.assertEqual(result["pulse"]["source"], "unknown")
            self.assertEqual(result["kpi"]["verdict"], "unknown")

    def test_missing_stamp_is_unknown(self):
        doc = stats(3)
        del doc["generatedAt"]
        with self.assertRaises(ValueError):
            world.evaluate(doc, BEFORE)

    def test_kst_offsets_normalize_to_utc(self):
        result = world.evaluate(stats(3, "2026-11-17T11:30:00+09:00"), BEFORE)
        self.assertEqual(result["pulse"]["measuredAt"], "2026-11-17T02:30:00Z")


if __name__ == "__main__":
    unittest.main()
