"""Privacy and CLI regressions; no database or production access."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("collector", Path(__file__).with_name("pg-slow-queries.py"))
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)
CANARY = "secret_inline_literal_174"


class CollectorTest(unittest.TestCase):
    def invoke(self, args, result):
        stdout, stderr = io.StringIO(), io.StringIO()
        with patch.object(collector.subprocess, "run", return_value=result) as run:
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                try:
                    code = collector.main(args)
                except SystemExit as exc:
                    code = exc.code
        self.assertNotIn(CANARY, stdout.getvalue() + stderr.getvalue())
        return code, stdout.getvalue(), stderr.getvalue(), run

    def row(self):
        row = {key: 1 for key in collector.METRICS}
        return dict(row, attribution=2, query=CANARY, extra=CANARY)

    def test_json_and_text_never_export_query_or_extra_fields(self):
        for args in ([], ["--json"], ["mean_exec_time", "10", "--json"]):
            with self.subTest(args=args):
                proc = subprocess.CompletedProcess([], 0, json.dumps([self.row()]), "")
                code, out, _, run = self.invoke(args, proc)
                self.assertEqual(code, 0)
                self.assertIn("ListFailureClusters", out)
                self.assertEqual(run.call_args.kwargs["timeout"], 15)
                command = run.call_args.args[0]
                self.assertIn("ON_ERROR_STOP=1", command)
                self.assertIn("SET statement_timeout='5s'", command[-1])
                if "--json" in args:
                    row = json.loads(out)[0]
                    self.assertEqual(set(row), set(collector.METRICS) | {"attributed_method", "source_file"})

    def test_unknown_attribution_does_not_echo_value(self):
        row = dict(self.row(), attribution=CANARY)
        code, out, _, _ = self.invoke(["--json"], subprocess.CompletedProcess([], 0, json.dumps([row]), ""))
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(out)[0]["attributed_method"], "Unknown / uncataloged")

    def test_invalid_arguments_never_start_database_or_echo_input(self):
        for args in ([CANARY], ["calls", "0"], ["calls", "101"],
                     ["calls", CANARY], ["--" + CANARY]):
            code, _, _, run = self.invoke(args, None)
            self.assertEqual(code, 2)
            run.assert_not_called()

    def test_query_builder_rejects_injection(self):
        for sort, limit in ((CANARY, 10), ("calls", "1; SELECT 1"), ("calls", 101)):
            with self.assertRaises(ValueError):
                collector.query_sql(sort, limit)

    def test_failed_or_malformed_database_response_is_sanitized(self):
        for proc in (subprocess.CompletedProcess([], 1, CANARY, CANARY),
                     subprocess.CompletedProcess([], 0, CANARY, ""),
                     subprocess.CompletedProcess([], 0, '{"query":"' + CANARY + '"}', ""),
                     subprocess.CompletedProcess([], 0, json.dumps([dict(self.row(), total_ms=CANARY)]), "")):
            code, _, _, _ = self.invoke(["--json"], proc)
            self.assertEqual(code, 1)

    def test_timeout_and_missing_process_are_sanitized(self):
        for error in (OSError(CANARY), subprocess.TimeoutExpired(CANARY, 15, stderr=CANARY)):
            with patch.object(collector.subprocess, "run", side_effect=error):
                with self.assertRaises(RuntimeError) as caught:
                    collector.run_sql("compose.yml", "SELECT 1")
                self.assertNotIn(CANARY, str(caught.exception))

    def test_empty_statistics_are_valid_json(self):
        code, out, _, _ = self.invoke(["--json"], subprocess.CompletedProcess([], 0, "[]", ""))
        self.assertEqual((code, json.loads(out)), (0, []))

    def test_check_is_read_only_and_init_is_explicit(self):
        proc = subprocess.CompletedProcess([], 0, '{"installed":true,"entries":0}', "")
        code, out, _, run = self.invoke(["--check"], proc)
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(out), {"installed": True, "entries": 0})
        self.assertEqual(run.call_count, 1)
        self.assertIn("pg_stat_statements(false)", run.call_args.args[0][-1])
        self.assertNotIn("CREATE EXTENSION", run.call_args.args[0][-1])
        code, _, _, run = self.invoke(["--init"], proc)
        self.assertEqual(code, 0)
        self.assertEqual(run.call_count, 2)
        self.assertIn("CREATE EXTENSION IF NOT EXISTS", run.call_args_list[0].args[0][-1])

    def test_compose_override_is_passed_as_one_argument(self):
        code, _, _, run = self.invoke(["--compose-file", "a path/compose.yml", "--json"],
                                     subprocess.CompletedProcess([], 0, "[]", ""))
        self.assertEqual(code, 0)
        self.assertEqual(run.call_args.args[0][3], "a path/compose.yml")


if __name__ == "__main__":
    unittest.main()
