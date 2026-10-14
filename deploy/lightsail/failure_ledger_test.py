#!/usr/bin/env python3
"""Behavioral regressions for the shipped collector, without Docker writes."""
import contextlib
import importlib.util
import io
import json
import os
import subprocess
import sys
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("ledger", os.path.join(os.path.dirname(__file__), "collect-failure-ledger.py"))
ledger = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ledger)

def page(rows=2, cursor="b", observations=2, unbalanced=0, current=None, budget=True):
    return dict(rows=rows, cursor=cursor, observations=observations, unbalanced=unbalanced,
                currentRows=rows if current is None else current, modern=0, withinJsonBudget=budget)

class LedgerTests(unittest.TestCase):
    def setUp(self):
        self.cap = patch.object(ledger, "PAGE_ROWS", 2)
        self.cap.start()
        self.addCleanup(self.cap.stop)

    def run_walk(self, pages, source=None, detail=None, indexed=True):
        calls = []
        def query(sql):
            calls.append(sql)
            if sql == ledger.PRIMARY_KEY_SQL:
                return dict(indexed=indexed, modern=False)
            if sql == ledger.SOURCE_SQL:
                if source is None:
                    self.fail("nonempty ledger read the source")
                return source
            if sql == ledger.DETAIL_SQL:
                return detail
            if not pages:
                raise ledger.Unmeasured("interrupted page")
            return pages.pop(0)
        return ledger.walk(query, "extended" if detail is not None else "settled"), calls

    def test_tail_invariant_is_not_discarded(self):
        proof, calls = self.run_walk([page(cursor="b"), page(cursor="d"), page(1,"e",1,1)])
        self.assertEqual((proof["rows"], proof["observations"], proof["unbalanced"], proof["pages"]), (5,5,1,3))
        self.assertIn("WHERE id > 'd'", calls[-1])
        self.assertIsNone(proof["fail"])

    def test_exact_multiple_needs_empty_terminal_page(self):
        proof, _ = self.run_walk([page(), page(0,None,0)])
        self.assertEqual((proof["rows"],proof["pages"]), (2,2))

    def test_no_prefix_totals_on_interruption(self):
        with self.assertRaises(ledger.Unmeasured):
            self.run_walk([page()])

    def test_oversized_historical_json_stays_unmeasured(self):
        with self.assertRaises(ledger.BudgetExceeded):
            self.run_walk([page(1,"a",0,current=0,budget=False)])

    def test_missing_pk_does_not_start_a_scan(self):
        with self.assertRaises(ledger.Unmeasured):
            self.run_walk([], indexed=False)

    def test_nonadvancing_cursor_is_not_exhaustion(self):
        with self.assertRaises(ledger.Unmeasured):
            self.run_walk([page(),page()])

    def test_source_only_when_entire_current_ledger_is_empty(self):
        proof,_ = self.run_walk([page(current=0,observations=0),page(1,"c",0,current=0)],dict(rows=1,fail=7))
        self.assertEqual((proof["rows"],proof["sourceRows"],proof["fail"]), (3,1,7))
        with self.assertRaises(ledger.BudgetExceeded):
            self.run_walk([page(0,None,0,current=0)],dict(rows=10001,fail=0))

    def test_detail_census_overflow_cannot_publish_cluster_totals(self):
        with self.assertRaises(ledger.BudgetExceeded):
            self.run_walk([page(1,"a",1)],detail=dict(complete=False))

    def test_cursor_quoting_and_first_empty_id(self):
        self.assertNotIn("WHERE id >", ledger.page_sql(None,False))
        self.assertIn("WHERE id > 'a''b'", ledger.page_sql("a'b",False))

    def test_numeric_and_boolean_proofs_fail_closed(self):
        for value in (True,1.5,"1",None):
            with self.assertRaises(ledger.Unmeasured):
                ledger.integer(value)
        for budget in ("true",1,None):
            with self.assertRaises(ledger.BudgetExceeded):
                self.run_walk([page(1,"a",1,budget=budget)])

    def test_commit_failure_suppresses_output_and_closes_session(self):
        class FakeSession:
            closed = False
            def query(self, sql):
                if sql == ledger.PRIMARY_KEY_SQL:
                    return dict(indexed=True,modern=False)
                return page(1,"a",1)
            def finish(self):
                raise ledger.Unmeasured("COMMIT failed")
            def close(self):
                self.closed = True
        session=FakeSession()
        with patch.object(ledger,"Session",return_value=session),patch.object(sys,"argv",["collector","settled"]):
            out=io.StringIO()
            with contextlib.redirect_stdout(out), self.assertRaises(SystemExit) as error:
                ledger.main()
            self.assertEqual(error.exception.code,3)
            self.assertEqual(out.getvalue(),"unavailable||||||||false|false\n")
            self.assertTrue(session.closed)


    def main_failure(self, error, mode):
        out = io.StringIO()
        with patch.object(ledger, "collect", side_effect=error), patch.object(sys, "argv", ["collector", mode]):
            with contextlib.redirect_stdout(out), self.assertRaises(SystemExit) as stopped:
                ledger.main()
        self.assertEqual(stopped.exception.code, 3)
        return out.getvalue()

    def test_unknown_read_errors_do_not_claim_confirmed_budget_exhaustion(self):
        errors = [OSError("private path"), ValueError("private SQL"),
                  ledger.Unmeasured("COMMIT failed"), subprocess.TimeoutExpired("private command", 1)]
        for error in errors:
            with self.subTest(error=type(error).__name__):
                out = self.main_failure(error, "extended")
                self.assertNotIn("collection-budget-exceeded", out)
                self.assertNotIn("private", out)
                self.assertNotIn("invariants=", out)

    def test_confirmed_budget_failure_uses_existing_status_without_prefix_totals(self):
        error = ledger.BudgetExceeded("private source")
        self.assertEqual(self.main_failure(error, "settled"), "budget-exceeded||||||||false|false\n")
        self.assertEqual(self.main_failure(error, "extended"), "detail_budget_status=collection-budget-exceeded\n")

    def test_elapsed_shared_deadline_is_confirmed_budget_exhaustion(self):
        session = ledger.Session.__new__(ledger.Session)
        session.deadline = time.monotonic() - 1
        with self.assertRaises(ledger.BudgetExceeded):
            session.remaining()

    def test_production_wrapper_only_forwards_a_confirmed_budget_marker(self):
        import shutil
        sh = shutil.which("sh")
        if not sh and os.path.isfile("C:/Program Files/Git/bin/sh.exe"):
            sh = "C:/Program Files/Git/bin/sh.exe"
        if not sh:
            self.skipTest("POSIX shell unavailable")
        path = os.path.join(os.path.dirname(__file__), "collect-production-evidence.sh")
        with open(path, encoding="utf-8") as stream:
            source = stream.read().replace("cd /opt/codesamplex/deploy", ":")
        for marker, expected in [
                ("", ""),
                ("detail_budget_status=collection-budget-exceeded", "detail_budget_status=collection-budget-exceeded\n"),
                ("private SQL error", "")]:
            with self.subTest(marker=marker):
                prelude = "docker() { printf '1\\n'; }\n"
                prelude += "csx_collect_failure_ledger() { printf '%s\\n' " + repr(marker) + "; return 3; }\n"
                result = subprocess.run([sh, "-c", prelude + source], capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 3)
                self.assertEqual(result.stdout, expected)


    def finish_fixture(self, child_code, seconds):
        process = subprocess.Popen([sys.executable, "-c", child_code],
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=subprocess.DEVNULL,
                                   creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
        session = ledger.Session.__new__(ledger.Session)
        session.identity = {"readOnly": True, "repeatableRead": True}
        session.query = lambda sql: session.identity
        session.send = lambda sql: None
        session.process = process
        session.deadline = time.monotonic() + seconds
        return session, process

    def test_final_process_wait_consumes_its_own_shared_budget(self):
        session, process = self.finish_fixture("import time; time.sleep(5)", 0.15)
        try:
            with self.assertRaises(ledger.BudgetExceeded):
                session.finish()
        finally:
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)
            for pipe in (process.stdin, process.stdout):
                if pipe is not None and not pipe.closed:
                    pipe.close()

    def test_nonzero_final_process_exit_is_not_a_confirmed_time_budget(self):
        session, process = self.finish_fixture("import sys; sys.exit(7)", 5)
        try:
            with self.assertRaises(ledger.Unmeasured) as stopped:
                session.finish()
            self.assertNotIsInstance(stopped.exception, ledger.BudgetExceeded)
        finally:
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)
            for pipe in (process.stdin, process.stdout):
                if pipe is not None and not pipe.closed:
                    pipe.close()


    def test_final_wait_budget_marker_never_publishes_partial_totals(self):
        session, process = self.finish_fixture("import time; time.sleep(5)", 0.15)
        session.selector = ledger.selectors.DefaultSelector()
        out = io.StringIO()
        try:
            with patch.object(ledger, "Session", return_value=session), \
                 patch.object(ledger, "walk", return_value={"rows": 999}), \
                 patch.object(sys, "argv", ["collector", "settled"]):
                with contextlib.redirect_stdout(out), self.assertRaises(SystemExit) as stopped:
                    ledger.main()
            self.assertEqual(stopped.exception.code, 3)
            self.assertEqual(out.getvalue(), "budget-exceeded||||||||false|false\n")
            self.assertIsNotNone(process.poll())
        finally:
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)
            for pipe in (process.stdin, process.stdout):
                if pipe is not None and not pipe.closed:
                    pipe.close()

    def test_existing_budgets_remain_fixed(self):
        self.assertEqual((ledger.SQL_SECONDS,ledger.COMMAND_SECONDS,ledger.JSON_BYTES,ledger.SOURCE_ROWS,ledger.DETAIL_ROWS),
                         (20,30,4096,10000,250000))

    @unittest.skipIf(sys.platform=="win32","production pipe selectors run on Linux")
    def test_partial_line_and_constructor_failure_do_not_escape_deadline(self):
        processes=[]
        real_popen=subprocess.Popen
        def launch(*args,**kwargs):
            self.assertIn("statement_timeout=20000", " ".join(args[0]))
            process=real_popen([sys.executable,"-u","-c","import sys,time;sys.stdout.write('{');sys.stdout.flush();time.sleep(5)"],**kwargs)
            processes.append(process)
            return process
        started=time.monotonic()
        with patch.object(ledger,"SQL_SECONDS",0.12),patch.object(ledger.subprocess,"Popen",side_effect=launch):
            with self.assertRaises(ledger.Unmeasured):
                ledger.Session()
        self.assertLess(time.monotonic()-started,1)
        self.assertIsNotNone(processes[0].poll())

    @unittest.skipIf(sys.platform=="win32","production pipe selectors run on Linux")
    def test_one_deadline_covers_all_queries(self):
        session=ledger.Session.__new__(ledger.Session)
        session.deadline=time.monotonic()-1
        with self.assertRaises(ledger.Unmeasured):
            session.query("SELECT 1")

if __name__ == "__main__":
    unittest.main()
