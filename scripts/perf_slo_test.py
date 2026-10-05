"""Fixture tests for scripts/perf-slo.py (#511). No network, no gh."""

import importlib.util
import json
import os
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("perf_slo", os.path.join(HERE, "perf-slo.py"))
slo = importlib.util.module_from_spec(spec)
spec.loader.exec_module(slo)


def entry(name, p95, target, errors=0, ttfb_p95=None, network=0.4):
    return {"name": name, "path": "/" + name, "count": 20, "errors": errors, "medianSeconds": p95 / 2,
            "p95Seconds": p95, "targetSeconds": target, "violated": p95 > target,
            "ttfbMedianSeconds": p95 / 2 + network,
            "ttfbP95Seconds": ttfb_p95 if ttfb_p95 is not None else p95 + network,
            "rttMedianSeconds": 0.1, "statuses": ["200"], "samples": []}


def result(*entries):
    return {"schemaVersion": 1, "measuredAt": "2026-09-26T00:00:00Z", "metric": "server_seconds",
            "paths": list(entries)}


def ok(server, ttfb=None):
    return {"ok": True, "server": server, "ttfb": ttfb if ttfb is not None else server + 0.4,
            "connect": 0.1, "status": 200}


def failed(status=503):
    return {"ok": False, "server": None, "ttfb": None, "status": status}


class Statistics(unittest.TestCase):
    def test_nearest_rank_p95_of_twenty_is_the_nineteenth(self):
        values = [float(i) for i in range(1, 21)]
        self.assertEqual(slo.p95(values), 19.0)
        self.assertEqual(slo.median(values), 10.5)

    def test_one_failure_in_twenty_does_not_move_p95(self):
        s = slo.summarize([ok(0.1)] * 19 + [failed()], timeout=10)
        self.assertEqual(s["errors"], 1)
        self.assertEqual(s["p95Seconds"], 0.1)

    def test_two_fast_failures_in_twenty_count_at_the_timeout(self):
        # A fast 503 must not look like a fast answer.
        s = slo.summarize([ok(0.1)] * 18 + [failed()] * 2, timeout=10)
        self.assertEqual(s["p95Seconds"], 10.0)
        self.assertEqual(s["statuses"], ["200", "503"])

    def test_slo_is_on_server_time_and_raw_ttfb_is_kept(self):
        # A far runner adds network time to every TTFB; the SLO number must
        # not move with it.
        near = slo.summarize([ok(0.05, ttfb=0.3)] * 20, timeout=10)
        far = slo.summarize([ok(0.05, ttfb=0.9)] * 20, timeout=10)
        self.assertEqual(near["p95Seconds"], far["p95Seconds"])
        self.assertEqual((near["ttfbP95Seconds"], far["ttfbP95Seconds"]), (0.3, 0.9))
        self.assertEqual(far["rttMedianSeconds"], 0.1)


class Probe(unittest.TestCase):
    def test_probe_sends_the_automated_user_agent(self):
        # internal/activity skips a User-Agent containing "monitor"; the
        # classification itself is tested there against every configured path.
        import http.server
        import threading
        seen = []

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                seen.append(self.headers.get("User-Agent"))
                self.send_response(200)
                self.send_header("Content-Length", "2")
                self.end_headers()
                self.wfile.write(b"{}")

            def log_message(self, *args):
                pass

        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.handle_request)
        thread.start()
        try:
            sample = slo.probe_once("http://127.0.0.1:%d" % server.server_port, "/healthz", 5)
        finally:
            thread.join(5)
            server.server_close()
        self.assertTrue(sample["ok"])
        self.assertEqual(seen, [slo.USER_AGENT])
        self.assertIn("monitor", slo.USER_AGENT.lower())


class Decision(unittest.TestCase):
    def test_single_violation_opens_nothing(self):
        self.assertEqual(slo.decide(entry("a", 0.9, 0.5), entry("a", 0.4, 0.5), None)[0], "none")

    def test_violation_without_a_previous_run_opens_nothing(self):
        self.assertEqual(slo.decide(entry("a", 0.9, 0.5), None, None)[0], "none")

    def test_two_consecutive_violations_open(self):
        self.assertEqual(slo.decide(entry("a", 0.9, 0.5), entry("a", 0.8, 0.5), None)[0], "open")

    def test_open_issue_still_violating_is_updated_not_duplicated(self):
        self.assertEqual(slo.decide(entry("a", 0.9, 0.5), entry("a", 0.8, 0.5), 42)[0], "update")

    def test_one_passing_run_does_not_close(self):
        self.assertEqual(slo.decide(entry("a", 0.4, 0.5), entry("a", 0.8, 0.5), 42)[0], "update")

    def test_two_consecutive_passes_close(self):
        self.assertEqual(slo.decide(entry("a", 0.4, 0.5), entry("a", 0.45, 0.5), 42)[0], "close")

    def test_pass_after_unknown_previous_does_not_close(self):
        self.assertEqual(slo.decide(entry("a", 0.4, 0.5), None, 42)[0], "update")

    def test_p95_equal_to_target_holds(self):
        self.assertFalse(entry("a", 0.5, 0.5)["violated"])
        self.assertEqual(slo.decide(entry("a", 0.5, 0.5), entry("a", 0.5, 0.5), 42)[0], "close")

    def test_full_sequence_opens_once_and_closes_once(self):
        runs = [0.4, 0.9, 0.9, 0.9, 0.4, 0.9, 0.4, 0.4, 0.4]
        issue, previous, log = None, None, []
        for p in runs:
            current = entry("a", p, 0.5)
            action, _ = slo.decide(current, previous, issue)
            if action == "open":
                issue = 7
            elif action == "close":
                issue = None
            log.append(action)
            previous = current
        self.assertEqual(log, ["none", "none", "open", "update", "update", "update", "update", "close", "none"])

    def test_plan_is_one_action_per_path_and_skips_untargeted(self):
        cur = result(entry("a", 0.9, 0.5), entry("b", 0.2, 0.5), {"name": "c", "targetSeconds": None})
        prev = result(entry("a", 0.9, 0.5), entry("b", 0.2, 0.5))
        actions = slo.plan(cur, prev, {"b": 11})
        self.assertEqual([(x["name"], x["action"], x["issue"]) for x in actions],
                         [("a", "open", None), ("b", "close", 11)])


class Reconcile(unittest.TestCase):
    """The gh side effects, with gh replaced by a recorder."""

    def run_reconcile(self, current, previous, listed):
        calls = []

        def fake_gh(argv):
            calls.append(argv)
            if argv[:2] == ["issue", "list"]:
                return json.dumps(listed)
            return ""

        original = slo.gh
        slo.gh = fake_gh
        try:
            with tempfile.TemporaryDirectory() as d:
                cur, prev = os.path.join(d, "cur.json"), os.path.join(d, "prev.json")
                slo.write_json(cur, current)
                slo.write_json(prev, previous)
                slo.main(["reconcile", "--result", cur, "--previous", prev, "--repo", "o/r"])
        finally:
            slo.gh = original
        return [c for c in calls if c[:2] != ["issue", "list"]]

    def test_second_violation_creates_one_marked_issue(self):
        calls = self.run_reconcile(result(entry("a", 0.9, 0.5)), result(entry("a", 0.9, 0.5)), [])
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][:2], ["issue", "create"])
        body = calls[0][calls[0].index("--body") + 1]
        self.assertIn(slo.marker("a"), body)
        self.assertIn("0.900s", body)

    def test_previous_result_on_another_metric_breaks_the_streak(self):
        prev = result(entry("a", 0.9, 0.5))
        prev["metric"] = "ttfb_seconds"
        calls = self.run_reconcile(result(entry("a", 0.9, 0.5)), prev, [])
        self.assertEqual(calls, [])

    def test_existing_marked_issue_is_commented_not_duplicated(self):
        listed = [{"number": 5, "body": slo.marker("a") + "\nold"}, {"number": 6, "body": "unrelated"}]
        calls = self.run_reconcile(result(entry("a", 0.9, 0.5)), result(entry("a", 0.9, 0.5)), listed)
        self.assertEqual([c[:3] for c in calls], [["issue", "comment", "5"]])

    def test_recovery_closes_the_marked_issue(self):
        listed = [{"number": 5, "body": slo.marker("a")}]
        calls = self.run_reconcile(result(entry("a", 0.3, 0.5)), result(entry("a", 0.3, 0.5)), listed)
        self.assertEqual([c[:3] for c in calls], [["issue", "close", "5"]])


class Baseline(unittest.TestCase):
    def test_target_is_baseline_p95_without_known_good(self):
        self.assertEqual(slo.choose_target(entry("a", 0.7, 1), None, None), (0.7, "baseline-p95"))

    def test_known_good_wins_when_the_same_vantage_already_violates_it(self):
        # Known-good is raw TTFB; the same vantage's TTFB p95 0.9 > 0.816.
        # Its network share is 0.4 s, so the server-time target is 0.416.
        kg = {"ttfbP95Seconds": 0.816}
        runner = entry("a", 0.3, 1, ttfb_p95=1.5, network=0.8)
        workstation = entry("a", 0.5, 1, ttfb_p95=0.9, network=0.4)
        self.assertEqual(slo.choose_target(runner, kg, workstation), (0.416, "known-good-p95"))

    def test_baseline_wins_when_the_same_vantage_is_within_known_good(self):
        # The runner's TTFB is over 0.816 only because it is far away.
        kg = {"ttfbP95Seconds": 0.816}
        runner = entry("a", 0.3, 1, ttfb_p95=1.5, network=0.8)
        workstation = entry("a", 0.2, 1, ttfb_p95=0.7)
        self.assertEqual(slo.choose_target(runner, kg, workstation), (0.3, "baseline-p95"))

    def test_baseline_command_records_date_commit_and_targets(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = os.path.join(d, "cfg.json")
            res = os.path.join(d, "res.json")
            slo.write_json(cfg, {"paths": [{"name": "a", "path": "/a"}]})
            r = result(entry("a", 0.3, 1))
            r.update(rounds=20, vantage="v", run="9", server={"version": "v1", "revision": "abc"})
            slo.write_json(res, r)
            slo.main(["baseline", "--config", cfg, "--result", res])
            with open(cfg, encoding="utf-8") as f:
                out = json.load(f)
        self.assertEqual(out["baseline"]["revision"], "abc")
        self.assertEqual(out["baseline"]["measuredAt"], "2026-09-26T00:00:00Z")
        self.assertEqual(out["paths"][0]["targetSeconds"], 0.3)


if __name__ == "__main__":
    unittest.main()
