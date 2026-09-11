"""Disposable local PG17 fixtures only. Never accepts a host, DSN or container."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import unittest
import uuid

ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("phase_tests", ROOT / "farm_sql_phases_test.py")
fixtures = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fixtures)
phases = fixtures.phases


def literal(text):
    assert "$fixture$" not in text
    return "$fixture$" + text + "$fixture$"


class PostgresTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.container = "csx-farm-phase-fixture-" + uuid.uuid4().hex
        cls.addClassCleanup(cls.cleanup)
        subprocess.run(("docker", "run", "--detach", "--name", cls.container, "--network", "none",
                        "--label", "csx.test=farm-sql-phases", "-e", "POSTGRES_HOST_AUTH_METHOD=trust",
                        "postgres:17-alpine", "postgres", "-c", "shared_preload_libraries=pg_stat_statements",
                        "-c", "compute_query_id=on", "-c", "track_activity_query_size=1024"),
                       check=True, timeout=120, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        for _ in range(40):
            ready = subprocess.run(("docker", "exec", cls.container, "pg_isready", "-U", "postgres"),
                                   timeout=3, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if ready.returncode == 0:
                break
            time.sleep(0.25)
        else:
            raise AssertionError("disposable fixture did not become ready")
        cls.sql("CREATE DATABASE csx;")
        cls.sql("CREATE EXTENSION pg_stat_statements;", database="csx")
        # Only this owned database: no mounted files, ports, application data or
        # production identities. Migrations populate an empty synthetic schema.
        names = subprocess.run(("git", "ls-tree", "-r", "--name-only", phases.CATALOG_REVISION,
                                "internal/serverstore/migrations"), cwd=ROOT.parent.parent,
                               check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5).stdout.decode().splitlines()
        for name in sorted(names):
            raw = subprocess.run(("git", "show", phases.CATALOG_REVISION+":"+name), cwd=ROOT.parent.parent,
                                 check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5).stdout
            cls.sql(raw.decode("utf-8"), database="csx")
        cls.sql("""CREATE TABLE fixture_activity(pid int,datid oid,usesysid oid,query_id bigint,query text,
          wait_event_type text,query_start timestamptz,datname text,state text);
          CREATE TABLE fixture_statements(dbid oid,userid oid,queryid bigint,query text);
          CREATE FUNCTION fixture_pgss(boolean) RETURNS SETOF fixture_statements LANGUAGE sql AS
          'SELECT * FROM fixture_statements';""", database="csx")

    @classmethod
    def cleanup(cls):
        if getattr(cls, "container", "").startswith("csx-farm-phase-fixture-"):
            subprocess.run(("docker", "rm", "--force", cls.container), timeout=15,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    @classmethod
    def sql(cls, sql, database="postgres"):
        result = subprocess.run(("docker", "exec", "-i", cls.container, "psql", "-X", "-U", "postgres",
                                 "-d", database, "-v", "ON_ERROR_STOP=1", "-Atq", "-f", "-"),
                                input=sql.encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
        if result.returncode:
            raise AssertionError("synthetic PostgreSQL fixture SQL failed: " + result.stderr.decode(errors="replace"))
        return result.stdout

    def setUp(self):
        self.sql("TRUNCATE fixture_activity,fixture_statements", database="csx")

    def sample(self, pgss=True):
        sql = phases.sampling_sql(pgss).replace("FROM pg_stat_activity WHERE", "FROM fixture_activity WHERE")
        sql = sql.replace("public.pg_stat_statements(true)", "fixture_pgss(true)")
        raw = self.sql("BEGIN READ ONLY; SET LOCAL statement_timeout=1000; " + sql + "; COMMIT;", database="csx")
        return phases.validate_sample(phases.decode(raw))

    def active(self, query, query_id=7, dbid=1, userid=2, pid=999999, wait="Lock"):
        self.sql("INSERT INTO fixture_activity VALUES (%d,%d,%d,%d,%s,%s,clock_timestamp()-interval '4 seconds','csx','active')" %
                 (pid, dbid, userid, query_id, literal(query), literal(wait)), database="csx")

    def representative(self, query, query_id=7, dbid=1, userid=2):
        self.sql("INSERT INTO fixture_statements VALUES (%d,%d,%d,%s)" %
                 (dbid, userid, query_id, "NULL" if query is None else literal(query)), database="csx")

    def test_real_pg17_normalization_for_every_current_query(self):
        queries = fixtures.source_queries()
        # Execute each actual family on the empty schema. The fixture changes
        # only bind values; pgss records real PostgreSQL normalized text.
        import re
        for phase, query in queries:
            expanded = re.sub(r"\$[0-9]+", "'2026-09-11T20:00:00Z'", query)
            self.sql(expanded, database="csx")
        raw = self.sql("SELECT COALESCE(json_agg(fingerprint),'[]') FROM (SELECT " +
                       phases.fingerprint_sql("s.query") + " AS fingerprint FROM public.pg_stat_statements(true) s "
                       "WHERE s.dbid=(SELECT oid FROM pg_database WHERE datname=current_database())) q", database="csx")
        actual = set(json.loads(raw))
        for digest, phase in phases.FINGERPRINTS:
            self.assertIn(digest, actual, phase)
        # The same lexer in the actual engine also matches all original Go SQL.
        for phase, query in queries:
            raw = self.sql("SELECT " + phases.fingerprint_sql(literal(query)), database="csx")
            self.assertEqual(raw.decode().strip(), fixtures.fingerprint(query), phase)

    def test_real_capabilities_and_extension_absence(self):
        caps = phases.validate_capabilities(phases.decode(self.sql(phases.CAPABILITY_SQL, database="csx")))
        self.assertEqual(caps["trackActivityQuerySize"], 1024)
        self.assertEqual(phases.variant(caps), "activity_with_pgss")
        absent = phases.validate_capabilities(phases.decode(self.sql(phases.CAPABILITY_SQL)))
        self.assertFalse(absent["pgssPublic"])
        self.assertEqual(phases.variant(absent), "activity_only")

    def test_actual_statement_excludes_own_active_backend(self):
        raw = self.sql("BEGIN READ ONLY; SET LOCAL statement_timeout=1000; " + phases.sampling_sql(True) + "; COMMIT;", database="csx")
        self.assertEqual(phases.validate_sample(phases.decode(raw))["active"], 0)

    def test_truncated_prefix_requires_matching_full_statement(self):
        query = next(q for p, q in fixtures.source_queries() if p == "backlog_stocks")
        self.active(query[:1023])
        self.assertEqual(self.sample()["unknown"]["truncated"], 1)
        self.representative(query)
        sample = self.sample()
        self.assertEqual(sample["phases"][0]["phase"], "backlog_stocks")
        self.assertEqual(sample["phases"][0]["waitClass"], "Lock")
        self.assertEqual(sample["active"], 1)
        self.assertEqual(self.sample(False)["unknown"]["truncated"], 1)

    def test_duplicate_representatives_count_active_once(self):
        query = next(q for p, q in fixtures.source_queries() if p == "backlog_stocks")
        self.active(query[:1023])
        self.representative(query)
        self.representative(query)
        result = self.sample()
        self.assertEqual(result["phases"][0]["count"], 1)

    def test_conflicting_unknown_or_null_representatives_stay_ambiguous(self):
        query = next(q for p, q in fixtures.source_queries() if p == "backlog_stocks")
        for other in ("SELECT 'private'", None, next(q for p, q in fixtures.source_queries() if p == "matrix")):
            with self.subTest(other_is_null=other is None):
                self.setUp()
                self.active(query[:1023])
                self.representative(query)
                self.representative(other)
                result = self.sample()
                self.assertEqual(result["unknown"]["ambiguous"], 1)
                self.assertEqual(result["phases"], [])

    def test_database_user_and_queryid_all_bound(self):
        query = next(q for p, q in fixtures.source_queries() if p == "backlog_stocks")
        self.active(query[:1023])
        for args in ({"dbid": 9}, {"userid": 9}, {"query_id": 9}):
            self.representative(query, **args)
        self.assertEqual(self.sample()["unknown"]["truncated"], 1)

    def test_utf8_boundary_and_absent_queryid_stay_truncated(self):
        # A multibyte character can leave the activity text below limit-1.
        # Query IDs may also be absent during a first/disabled collection.
        query = "--" + "x"*1018
        self.active(query, query_id=0)
        self.assertEqual(self.sample()["unknown"]["truncated"], 1)

    def test_wait_classes_age_cap_and_private_unknowns(self):
        query = next(q for p, q in fixtures.source_queries() if p == "claims")
        self.active(query, wait="new-private-wait-value")
        self.sql("UPDATE fixture_activity SET query_start=clock_timestamp()-interval '2 days'", database="csx")
        row = self.sample()["phases"][0]
        self.assertEqual(row["waitClass"], "other")
        self.assertEqual(row["minAgeMs"], 86400000)
        self.assertTrue(row["ageCapped"])
        self.active("SELECT 'private-customer-value'", pid=999998)
        result = self.sample()
        self.assertEqual(result["unknown"]["unclassified"], 1)
        self.assertNotIn("private", json.dumps(result))

    def test_complete_activity_does_not_read_pgss(self):
        query = next(q for p, q in fixtures.source_queries() if p == "claims")
        self.active(query)
        self.sql("CREATE OR REPLACE FUNCTION fixture_pgss(boolean) RETURNS SETOF fixture_statements LANGUAGE plpgsql AS "
                 "$$ BEGIN RAISE EXCEPTION 'must not read statement text'; END $$", database="csx")
        try:
            self.assertEqual(self.sample()["phases"][0]["phase"], "claims")
        finally:
            self.sql("CREATE OR REPLACE FUNCTION fixture_pgss(boolean) RETURNS SETOF fixture_statements LANGUAGE sql AS "
                     "'SELECT * FROM fixture_statements'", database="csx")

    def test_lexer_on_real_pg_handles_quotes_comments_and_identifier_digits(self):
        for query in ("SELECT 'a--b''c', 12 -- unmatched ' comment\nFROM table1",
                      'SELECT "column1" FROM t', "SELECT $$private$$", "SELECT column2 FROM table1"):
            raw = self.sql("SELECT " + phases.fingerprint_sql(literal(query)), database="csx")
            self.assertEqual(raw.decode().strip() or None, fixtures.fingerprint(query))


if __name__ == "__main__":
    unittest.main()
