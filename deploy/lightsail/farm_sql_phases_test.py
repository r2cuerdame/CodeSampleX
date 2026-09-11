"""Offline source, transport, bounded-clock and strict-schema regressions."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("phase_reader", ROOT / "read-farm-sql-phases.py")
reader = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(reader)
phases, transport = reader.phases, reader.transport


def source_queries():
    """Evaluate only Go string/constant concatenation, never application code."""
    folder = ROOT.parent.parent / "internal" / "serverstore"
    # A later server change must not silently retarget this diagnostic. Read
    # the immutable catalog commit, also available in canonical full checkouts.
    files = {name: subprocess.run(("git", "show", phases.CATALOG_REVISION+":internal/serverstore/"+name),
             cwd=folder, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5, check=True).stdout.decode("utf-8")
             for name in ("farm_pg.go", "farmbacklog_pg.go", "dependencyclosure_pg.go", "completenessgaps.go", "matrixcells_pg.go")}
    constants = {}
    token = re.compile(r'\s*(`[^`]*`|"(?:\\.|[^"\\])*"|[A-Za-z_][A-Za-z_0-9]*)')

    def expression(text, pos):
        result = ""
        while True:
            match = token.match(text, pos)
            if match is None:
                raise AssertionError("unsupported Go catalog expression")
            value = match[1]
            if value.startswith("`"):
                result += value[1:-1]
            elif value.startswith('"'):
                result += json.loads(value)
            else:
                if value not in constants:
                    found = [(s, m.end()) for s in files.values() for m in re.finditer(r"\b"+re.escape(value)+r"\s*=\s*", s)]
                    if len(found) != 1:
                        raise AssertionError("constant absent or ambiguous")
                    constants[value] = expression(*found[0])
                result += constants[value]
            pos = match.end()
            add = re.match(r"\s*\+", text[pos:])
            if add is None:
                return result
            pos += add.end()

    queries = []
    text = files["farm_pg.go"]
    for match in re.finditer(r"(?:c|tx)\.Query(?:Row)?\(ctx,\s*", text):
        phase = "workers" if match.start() < text.index("func (p *PG) FarmHealthNow") else (
            "health" if match.start() < text.index("func (p *PG) FarmCoverage") else "coverage")
        queries.append((phase, expression(text, match.end())))
    text = files["farmbacklog_pg.go"]
    queries.extend((phase, expression(text, match.end())) for phase, match in zip(
        ("backlog_stocks", "claims", "first_pass", "completeness"), re.finditer(r"tx\.Query(?:Row)?\(ctx,\s*", text)))
    queries.append(("matrix", expression("matrixCellsQuery", 0)))
    return queries


def normalized(query):
    pattern = phases.TOKEN_PATTERN.replace("[[:space:]]", r"\s")
    tokens = re.findall(pattern, query)
    if "".join(tokens) != query:
        return None
    out = []
    for token in tokens:
        if token.startswith("--") or token.isspace():
            continue
        out.append("?" if token.startswith(("'", "$")) or token.isdigit() else token.lower())
    return " ".join(out)


def fingerprint(query):
    value = normalized(query)
    return hashlib.sha256(value.encode()).hexdigest() if value is not None else None


def encoded(value):
    return (json.dumps(value, separators=(",", ":"))+"\n").encode()


IDENTITY = {"id": "a"*64, "imageDigest": "sha256:"+"b"*64, "revision": phases.CATALOG_REVISION,
            "startedAt": "2026-09-11T20:00:00Z"}
CAPABILITIES = {"trackActivityQuerySize": 1024, "computeQueryId": "auto", "pgssPublic": True, "pgssPreloaded": True}
COUNTS = {"active": 1, "unknown": {"unclassified": 0, "truncated": 1, "ambiguous": 0}, "phases": []}


class FakeClock:
    def __init__(self):
        self.now = 0.0
        self.base = transport.timestamp_ns("2026-09-11T21:00:00Z")

    def monotonic(self):
        return self.now

    def wall(self):
        return self.base + int(self.now * 1000000000)

    def sleep(self, seconds):
        self.now += seconds


class PhaseTests(unittest.TestCase):
    def collect(self, mutate=None, delay=0.01):
        clock, calls = FakeClock(), []
        def run(argv, timeout, limit):
            calls.append(argv)
            self.assertLessEqual(timeout, 3)
            self.assertEqual(limit, phases.MAX_COMMAND_BYTES)
            clock.now += delay
            result = IDENTITY if argv == transport.INSPECT else (
                CAPABILITIES if argv[-1] == phases.CAPABILITY_SQL else COUNTS)
            return encoded(mutate(len(calls), copy.deepcopy(result)) if mutate else result)
        result = phases.collect(phases.CATALOG_REVISION, run, clock.monotonic, clock.wall, clock.sleep)
        return result, calls, clock

    def test_complete_catalog_matches_pinned_source(self):
        expected = tuple((fingerprint(query), phase) for phase, query in source_queries())
        self.assertEqual(len(expected), 14)
        self.assertNotIn(None, [item[0] for item in expected])
        self.assertEqual(len(set(x[0] for x in expected)), len(expected))
        self.assertEqual(phases.FINGERPRINTS, expected)
        self.assertEqual(phases.CATALOG_REVISION, "2b859134e5a9708a76be6e148af2034d9a57cc5e")

    def test_truncated_shared_prefixes_never_fingerprint(self):
        known = dict(phases.FINGERPRINTS)
        for _, query in source_queries():
            for limit in (100, 1023):
                if len(query.encode()) > limit:
                    self.assertNotIn(fingerprint(query[:limit]), known)
        self.assertNotIn(fingerprint("SELECT 1; SELECT 2"), known)

    def test_lexing_preserves_identifier_digits_and_literal_comments(self):
        self.assertNotEqual(fingerprint("SELECT thing1 FROM t"), fingerprint("SELECT thing2 FROM t"))
        self.assertNotEqual(fingerprint("SELECT a b FROM t"), fingerprint("SELECT ab FROM t"))
        self.assertEqual(fingerprint("SELECT 'a--b''c', 12 -- comment ' unmatched\nFROM t"), fingerprint("SELECT $1, $2 FROM t"))
        self.assertIsNone(fingerprint('SELECT "column1" FROM t'))
        self.assertIsNone(fingerprint("SELECT $$secret$$"))

    def test_available_ninety_seconds_and_bounded_cadence(self):
        result, calls, clock = self.collect()
        self.assertEqual(result["availability"], "available")
        self.assertLessEqual(len(result["samples"]), 45)
        self.assertGreaterEqual(len(result["samples"]), 43)
        self.assertGreaterEqual(result["window"]["elapsedMs"], 90000)
        self.assertLess(clock.now, 105)
        self.assertEqual(calls[0], transport.INSPECT)
        self.assertEqual(calls[-1], transport.INSPECT)

    def test_changed_identity_clears_all_measurements(self):
        result, _, _ = self.collect(lambda index, value: dict(value, id="c"*64) if "id" in value and index > 1 else value)
        self.assertEqual(result["failureClass"], "identity_changed")
        self.assertIsNone(result["samples"])
        self.assertIsNone(result["capabilities"])

    def test_initial_revision_mismatch_prevents_sql(self):
        result, calls, _ = self.collect(lambda index, value: dict(value, revision="f"*40) if index == 1 else value)
        self.assertEqual(result["failureClass"], "revision_mismatch")
        self.assertEqual(len(calls), 1)

    def test_invalid_capabilities_are_unavailable_not_zero(self):
        result, calls, _ = self.collect(lambda index, value: dict(value, trackActivityQuerySize=True) if index == 2 else value)
        self.assertEqual(result["failureClass"], "invalid_capabilities")
        self.assertEqual(len(calls), 2)
        self.assertIsNone(result["samples"])

    def test_malformed_sample_stops_without_retry_and_clears(self):
        result, calls, _ = self.collect(lambda index, value: dict(value, raw="private") if index == 4 else value)
        self.assertEqual(result["failureClass"], "invalid_sample")
        self.assertEqual(len(calls), 4)
        self.assertIsNone(result["samples"])
        self.assertNotIn("private", json.dumps(result))

    def test_revision_rejected_before_commands(self):
        for value, reason in (("x", "invalid_expected_revision"), ("a"*40, "unsupported_catalog")):
            result = phases.collect(value, lambda *args: self.fail("must not run"))
            self.assertEqual(result["failureClass"], reason)

    def test_failures_stop_first_command(self):
        calls = []
        def fail(*args):
            calls.append(args)
            raise transport.Unavailable("command_timeout")
        result = phases.collect(phases.CATALOG_REVISION, fail)
        self.assertEqual(result["failureClass"], "command_timeout")
        self.assertEqual(len(calls), 1)

    def test_slow_samples_do_not_burst_or_extend_total_budget(self):
        result, calls, clock = self.collect(delay=2.5)
        self.assertEqual(result["availability"], "available")
        self.assertLess(len(result["samples"]), 45)
        self.assertLessEqual(clock.now, 105)
        self.assertTrue(all(b["offsetMs"]-a["offsetMs"] >= 2500 for a,b in zip(result["samples"], result["samples"][1:])))

    def test_wall_clock_step_invalidates_measurements(self):
        clock, calls = FakeClock(), []
        def run(argv, *_):
            calls.append(argv)
            value = IDENTITY if argv == transport.INSPECT else (CAPABILITIES if argv[-1] == phases.CAPABILITY_SQL else COUNTS)
            if len(calls) == 4:
                clock.base -= 2000000000
            return encoded(value)
        result = phases.collect(phases.CATALOG_REVISION, run, clock.monotonic, clock.wall, clock.sleep)
        self.assertEqual(result["failureClass"], "clock_changed")
        self.assertIsNone(result["samples"])

    def test_readonly_init_retains_unavailable_without_transport(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {
                "GITHUB_SHA": "b"*40, "GITHUB_RUN_ID": "123", "EXPECTED_REVISION": phases.CATALOG_REVISION}), \
                mock.patch.object(reader, "remote", side_effect=AssertionError("no remote init")):
            original = Path.cwd()
            try:
                os.chdir(directory)
                self.assertEqual(reader.main(True), 0)
                artifact = json.loads(Path("farm-sql-phases.json").read_text())
                self.assertEqual(artifact["diagnostic"]["failureClass"], "not_collected")
            finally:
                os.chdir(original)

    def test_strict_schema_and_types(self):
        for mutate in (lambda x: dict(x, active=True), lambda x: dict(x, active=129),
                       lambda x: dict(x, unknown={"unclassified": 0, "truncated": 0, "ambiguous": 0}),
                       lambda x: dict(x, phases=[{"query": "secret"}])):
            with self.assertRaises(Exception):
                phases.validate_sample(mutate(copy.deepcopy(COUNTS)))
        for raw in (b'{"x":1,"x":2}\n', b'{"x":NaN}\n', b'{}', b'\xff\n', b'{}\n{}\n'):
            with self.assertRaises(Exception):
                phases.decode(raw)

    def test_bounded_transport_discards_stderr_and_limits_stdout(self):
        raw = transport.bounded_command((sys.executable, "-I", "-c", "import sys; sys.stderr.write('private'); print('{}')"), 3, 32)
        self.assertEqual(raw, b"{}\n" if os.name != "nt" else b"{}\r\n")
        with self.assertRaises(transport.Unavailable) as caught:
            transport.bounded_command((sys.executable, "-I", "-c", "print('private'*1000)"), 3, 32)
        self.assertEqual(caught.exception.reason, "byte_limit")
        with self.assertRaises(transport.Unavailable) as caught:
            transport.bounded_command((sys.executable, "-I", "-c", "import time; time.sleep(2)"), 0.1, 32)
        self.assertEqual(caught.exception.reason, "command_timeout")

    def test_capability_fallback_is_activity_only(self):
        for field, value in (("pgssPublic", False), ("pgssPreloaded", False), ("computeQueryId", "off")):
            self.assertEqual(phases.variant(dict(CAPABILITIES, **{field: value})), "activity_only")
        self.assertNotIn("public.pg_stat_statements", phases.sampling_sql(False))

    def test_fixed_sql_route_and_privileges(self):
        command = phases.sql_command(phases.CAPABILITY_SQL)
        self.assertEqual(command[:6], ("docker", "compose", "-f", "/opt/codesamplex/deploy/docker-compose.yml", "exec", "-T"))
        self.assertIn("default_transaction_read_only=on", " ".join(command))
        self.assertIn("statement_timeout=1000", " ".join(command))
        self.assertIn("-X", command)
        for sql in (phases.CAPABILITY_SQL, phases.sampling_sql(False), phases.sampling_sql(True)):
            self.assertNotRegex(sql.lower(), r"\b(create|alter|update|delete|insert|truncate|reset|set_config|pg_sleep)\b")
        sql = phases.sampling_sql(True)
        self.assertIn("state='active' AND pid<>pg_backend_pid()", sql)
        self.assertIn("s.dbid=n.datid AND s.userid=n.usesysid AND s.queryid=n.query_id", sql)
        self.assertIn("CASE WHEN EXISTS (SELECT 1 FROM needed)", sql)

    def test_remote_bootstrap_does_not_run_funnel(self):
        source = reader.remote_source().decode()
        tree = compile(source, "<fixture>", "exec")
        with mock.patch.object(sys, "argv", ["stdin", "invalid"]), mock.patch("builtins.print") as emit:
            exec(tree, {"__name__": "__main__"})
        result = json.loads(emit.call_args[0][0])
        self.assertEqual(result["catalogRevision"], phases.CATALOG_REVISION)
        self.assertEqual(result["failureClass"], "invalid_expected_revision")

    def test_transport_fixed_pinned_stdin_only_and_no_error_output(self):
        with tempfile.TemporaryDirectory() as directory:
            key = Path(directory) / "key"
            key.write_text("fixture")
            env = {"EXPECTED_REVISION": phases.CATALOG_REVISION, "PRODUCTION_HOST": "example.test",
                   "PRODUCTION_USER": "ubuntu", "PRODUCTION_KEY_PATH": str(key), "PRODUCTION_KNOWN_HOSTS_PATH": str(key)}
            calls = []
            reader.remote(env, lambda *args, **kwargs: calls.append((args, kwargs)) or b"{}\n")
            args, kwargs = calls[0]
            self.assertEqual(args[0][-1], reader.REMOTE_COMMAND+" "+phases.CATALOG_REVISION)
            self.assertIn("StrictHostKeyChecking=yes", args[0])
            self.assertEqual(args[0][1:3], ("-F", "/dev/null"))
            self.assertTrue(kwargs["source"].startswith(b"import sys, types\n"))
            self.assertLessEqual(len(kwargs["source"]), 65536)
            for invalid in ("bad; touch /tmp/x", "-oProxyCommand=x", "a\nb"):
                with self.assertRaises(Exception):
                    reader.remote(dict(env, PRODUCTION_HOST=invalid), lambda *args: self.fail("must not call SSH"))
            failed = reader.diagnose(env, lambda _: (_ for _ in ()).throw(ValueError("private material")))
            self.assertNotIn("private", json.dumps(failed))

    def test_workflow_gates_manual_ci_and_retained_artifact(self):
        workflow = (ROOT.parent.parent / ".github/workflows/farm-sql-phases.yml").read_text()
        for text in ("workflow_dispatch:", "test \"$GITHUB_REF\" = refs/heads/main", "group: codesamplex-production",
                     "cancel-in-progress: false", "environment: codesamplex-production", ".head_sha == $sha",
                     ".head_repository.full_name == $repo", "--initialize", "if: always()", "farm-sql-phases.json"):
            self.assertIn(text, workflow)
        self.assertNotIn("schedule:", workflow)
        self.assertLess(workflow.index("Require successful exact canonical main CI"), workflow.index("SSH_PRIVATE_KEY:"))

    def test_fixture_waits_for_final_tcp_listener_before_sql(self):
        spec = importlib.util.spec_from_file_location("phase_pg_fixture", ROOT / "farm_sql_phases_pg_test.py")
        fixture = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(fixture)
        class StopFixture(Exception):
            pass
        calls = []
        def run(argv, **kwargs):
            calls.append(argv)
            if argv[1] == "run":
                return subprocess.CompletedProcess(argv, 0)
            self.assertIn("pg_isready", argv)
            self.assertIn("-h", argv)
            self.assertEqual(argv[argv.index("-h")+1], "127.0.0.1")
            raise StopFixture
        # Run the real fixture's setup only to its first readiness attempt,
        # with all process calls replaced. No container/SQL/cleanup is run.
        with mock.patch.object(fixture.subprocess, "run", side_effect=run), \
                mock.patch.object(fixture.PostgresTests, "addClassCleanup"):
            with self.assertRaises(StopFixture):
                fixture.PostgresTests.setUpClass()
        self.assertEqual(len(calls), 2)


if __name__ == "__main__":
    if sys.argv[1:] == ["--catalog"]:
        print(repr(tuple((fingerprint(query), phase) for phase, query in source_queries())))
    else:
        unittest.main()
