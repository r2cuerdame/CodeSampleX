"""Failure-state tests for the host supervisor; no Docker/DB/production access."""
import importlib.util
import inspect
import json
import os
import sys
import time
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("migration", Path(__file__).with_name("offline-migration.py"))
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)
OWNER = "a" * 32
TARGET = "b" * 40
PREVIOUS = "c" * 40
CONTROL = "d" * 40
RELEASE = "v1.2.3"
IMAGE = "sha256:" + "e" * 64
SERVER_BACKEND_ROW = {
    "pid": 2718, "backendStart": "2026-09-09 01:00:00+00",
    "queryStart": "2026-09-09 01:00:01+00", "applicationName": "",
    "userName": "csx", "clientAddress": "172.20.0.4", "queryHash": "0" * 32,
}


class FakeHost(migration.Host):
    def __init__(self, root):
        super().__init__(OWNER, root)
        self.calls = []
        self.helper_present = True
        self.foreign_helper = False
        self.backend_present = True
        self.cancel_clears = False
        self.terminate_clears = True
        self.progress = 0
        self.rollback_failure = None
        self.helper_running = False
        self.helper_exit = 0
        self.stale = 0
        self.barrier_rearmed = False
        self.barrier_count = 1
        self.barrier_armed = False
        self.barrier_rearm_sticks = True
        self.ledger_version = None
        self.index_fault = None
        self.review_note_column = {"type": "text", "nullable": "NO", "default": "''::text"}
        self.credential_adoption_columns = [
            {"table": "anonymous_analytics_collection", "name": "credential_adoption_started_at",
             "type": "timestamp with time zone", "nullable": "NO", "default": "now()"},
            {"table": "anonymous_client_days", "name": "credential_issued_count",
             "type": "bigint", "nullable": "NO", "default": "0"},
            {"table": "anonymous_client_days", "name": "credential_present_count",
             "type": "bigint", "nullable": "NO", "default": "0"},
        ]
        self.server_present = False
        self.server_backend_present = False
        self.server_image = IMAGE
        self.served_revision = TARGET
        self.release_tag = RELEASE
        self.health = "ok"
        self.proxy_revision = TARGET
        self.proxy_status = "200"
        self.helper_environment = {"CSX_DSN": "postgres://db/csx?application_name=" + self.application,
                                   "PGAPPNAME": self.application}
        self.after = {"samples": 1, "receipts": 2, "pass": 3, "fail": 4}
        self.evidence["sourceBefore"] = self.after.copy()

    def command(self, args, seconds=30, check=True, environment=None):
        self.calls.append(("command", args))
        if args[0] == "sh" and Path(args[1]).name == self.rollback_failure:
            raise RuntimeError("injected rollback failure")
        if args[0] == "curl":
            body = (json.dumps({"revision": self.proxy_revision}) if args[-1].endswith("/version")
                    else ("ok" if args[-1].endswith("/healthz") else '<link rel="canonical" href="https://codesamplex.dev/features">'))
            return subprocess.CompletedProcess(args, 0, body + "\n" + self.proxy_status, "")
        return subprocess.CompletedProcess(args, 0, "", "")

    def docker(self, *args, seconds=30, check=True, environment=None):
        self.calls.append(("docker", args))
        if args == ("inspect", "codesamplex-server-1"):
            if not self.server_present:
                return subprocess.CompletedProcess(args, 1, "", "")
            return subprocess.CompletedProcess(args, 0, json.dumps([{
                "Image": self.server_image,
                "State": {"StartedAt": "2026-09-09T00:59:00Z"},
                "NetworkSettings": {"Networks": {"default": {"IPAddress": "172.20.0.4"}}}}]), "")
        if args[:2] == ("ps", "-aq"):
            output = "helper-id" if self.helper_present else ""
        elif args[-1] == "http://127.0.0.1:8080/healthz":
            output = self.health
        elif args[-1] == "http://127.0.0.1:8080/version":
            output = json.dumps({"revision": self.served_revision})
        elif args[-1] == "/data/dist/.release-tag":
            output = self.release_tag
        elif args[:1] == ("rm",):
            self.helper_present = False
            output = ""
        else:
            output = ""
        return subprocess.CompletedProcess(args, 0, output, "")

    def inspect(self, name):
        return {"Image": IMAGE, "Config": {"Env": ["CSX_DSN=postgres://db/csx?application_name=" + self.application],
            "Labels": {
            "codesamplex.deploy-owner": "foreign" if self.foreign_helper else OWNER}},
            "State": {"Running": True if name == "codesamplex-server-1" else self.helper_running,
                      "ExitCode": self.helper_exit, "OOMKilled": False,
                      "StartedAt": "2026-09-09T01:00:00Z"}}

    def clients(self, owned_only=False):
        if not self.backend_present:
            return []
        return [{"pid": 1729, "backendStart": "2026-09-09 01:00:00+00",
                 "queryStart": "2026-09-09 01:00:01+00",
                 "applicationName": self.application, "queryHash": "f" * 32}]

    def query(self, sql):
        self.calls.append(("sql", sql))
        if "credential_adoption_started_at" in sql and "information_schema.columns" in sql:
            return self.credential_adoption_columns
        if "information_schema.columns" in sql:
            return self.review_note_column
        if "client_addr=ANY" in sql:
            return ([{"pid": 2718, "backendStart": "2026-09-09 01:00:00+00",
                      "queryStart": "2026-09-09 01:00:01+00", "applicationName": "",
                      "userName": "csx", "clientAddress": "172.20.0.4",
                      "queryHash": "0" * 32}] if self.server_backend_present else [])
        if "pg_terminate_backend" in sql and "pid=2718" in sql:
            self.server_backend_present = False
            return [True]
        if "pg_cancel_backend" in sql:
            if self.cancel_clears:
                self.backend_present = False
            return [True]
        if "pg_terminate_backend" in sql:
            if self.terminate_clears:
                self.backend_present = False
            return [True]
        if "pg_stat_progress_create_index" in sql:
            return self.progress
        if "max(version)" in sql:
            exp_mig = self.config.get("expectedMigration", "0036_builder_projections.sql")
            target_count = migration.REVIEWED_MIGRATIONS[exp_mig]["count"] if exp_mig in migration.REVIEWED_MIGRATIONS else 37
            if self.ledger_version is not None:
                return {"version": self.ledger_version, "count": target_count - 1}
            return {"version": exp_mig, "count": target_count}
        if "pg_get_indexdef" in sql:
            exp_mig = self.config.get("expectedMigration", "0036_builder_projections.sql")
            target_indexes = migration.REVIEWED_MIGRATIONS.get(exp_mig, {}).get("indexes", migration.INDEXES)
            rows = [{"name": name, "valid": True, "ready": True, "definition": value}
                    for name, value in target_indexes.items()]
            if self.index_fault == "missing": rows.pop()
            if self.index_fault == "wrong": rows[0]["definition"] += " WHERE false"
            if self.index_fault == "invalid": rows[0]["valid"] = False
            if self.index_fault == "not-ready": rows[0]["ready"] = False
            return rows
        if "UPDATE stats_daily" in sql:
            self.barrier_rearmed = True
            if self.barrier_rearm_sticks and self.barrier_count == 1:
                self.barrier_armed = True
            return {"count": self.barrier_count, "day": "2026-09-09" if self.barrier_count else None,
                    "armed": self.barrier_armed if self.barrier_count else None}
        if "builderRepairRequired" in sql:
            return {"count": self.barrier_count, "day": "2026-09-09" if self.barrier_count else None,
                    "armed": self.barrier_armed if self.barrier_count else False}
        if "'repairRequired'" in sql:
            return {"samples": self.stale, "receipts": 0, "repairRequired": True}
        raise AssertionError("unexpected SQL")

    def source_totals(self):
        return self.after.copy()

    def verify_image(self, name, image, revision):
        self.calls.append(("identity", (name, image, revision)))
        return self.inspect(name)


class SupervisorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / "deploy"
        self.state = self.root / (".migration-" + OWNER)
        self.state.mkdir(parents=True)
        lock = self.root.parent / ".deploy-lock"
        lock.mkdir()
        (lock / "owner").write_text(OWNER)
        (self.state / "config.json").write_text(json.dumps({
            "targetSha": TARGET, "previousSha": PREVIOUS, "operationalSha": CONTROL,
            "imageDigest": IMAGE, "previousImageDigest": "sha256:" + "f" * 64,
            "expectedMigration": "0036_builder_projections.sql", "migrationTimeoutSeconds": 60,
            "expectedReleaseTag": RELEASE}))
        self.host = FakeHost(self.root)
        self.clock = iter(range(0, 100000, 3))
        self.addCleanup(patch.stopall)
        patch.object(migration.time, "sleep", lambda _: None).start()
        patch.object(migration.time, "monotonic", lambda: next(self.clock)).start()

    def test_docker_stop_does_not_count_as_database_cleanup(self):
        self.host.cleanup_helper()
        signals = [s for kind, s in self.host.calls if kind == "sql" and "_backend" in s]
        self.assertEqual(len(signals), 2)
        self.assertIn("pg_cancel_backend", signals[0])
        self.assertIn("pg_terminate_backend", signals[1])
        for sql in signals:
            self.assertIn("pid=1729", sql)
            self.assertIn("backend_start='2026-09-09 01:00:00+00'::timestamptz", sql)
            self.assertIn("application_name='csx-migrate-" + OWNER + "'", sql)
        self.assertFalse(self.host.helper_present)
        self.assertFalse(self.host.backend_present)
        self.assertEqual(self.host.evidence["cleanup"], "pass")

    def test_cancel_that_clears_session_needs_no_terminate(self):
        self.host.cancel_clears = True
        self.host.cleanup_helper()
        self.assertFalse(any(k == "sql" and "pg_terminate_backend" in v for k, v in self.host.calls))

    def test_foreign_helper_is_never_stopped_or_removed(self):
        self.host.foreign_helper = True
        with self.assertRaisesRegex(RuntimeError, "ownership mismatch"):
            self.host.finalize()
        self.assertFalse(any(k == "docker" and v[0] in ("stop", "rm") for k, v in self.host.calls))
        self.assertFalse(any(k == "command" for k, _ in self.host.calls))

    def test_surviving_backend_blocks_rollback(self):
        self.host.terminate_clears = False
        with self.assertRaisesRegex(RuntimeError, "survived termination"):
            self.host.finalize()
        self.assertFalse(any(k == "command" for k, _ in self.host.calls))
        self.assertNotEqual(self.host.evidence["cleanup"], "pass")

    def test_surviving_index_ddl_blocks_rollback(self):
        self.host.progress = 1
        with self.assertRaisesRegex(RuntimeError, "index DDL remains"):
            self.host.finalize()
        self.assertFalse(any(k == "command" for k, _ in self.host.calls))

    def test_recovery_cleanup_timeout_remains_blocking_without_proof(self):
        def timeout():
            raise RuntimeError("host operation deadline exceeded")
        self.host.stop_server_for_rollback = timeout
        with self.assertRaisesRegex(RuntimeError, "deadline exceeded"):
            self.host.finalize()
        self.assertFalse(any(k == "command" for k, _ in self.host.calls))

    def test_finalizer_reloads_durable_state_and_rolls_back_in_order(self):
        self.host.save(phase="migrating", serverStopStarted=True)
        restarted = FakeHost(self.root)
        restarted.finalize()
        commands = [Path(v[1]).name for k, v in restarted.calls if k == "command"]
        self.assertEqual(commands, ["rollback-server.sh", "rollback-caddy.sh"])
        self.assertEqual(restarted.evidence["phase"], "rolled-back")
        self.assertEqual(restarted.evidence["rollback"], "succeeded")

    def test_caddy_rollback_is_attempted_even_if_server_rollback_fails(self):
        self.host.rollback_failure = "rollback-server.sh"
        with self.assertRaisesRegex(RuntimeError, "exact rollback failed"):
            self.host.finalize()
        commands = [Path(v[1]).name for k, v in self.host.calls if k == "command"]
        self.assertEqual(commands, ["rollback-server.sh", "rollback-caddy.sh"])
        self.assertEqual(self.host.evidence["rollback"], "failed")

    def test_finalizer_is_idempotent_after_commit_or_rollback(self):
        for phase in ("committed", "rolled-back", "rolled-back-degraded"):
            self.host.save(phase=phase)
            self.host.calls.clear()
            self.host.finalize()
            self.assertEqual(self.host.calls, [])

    def test_controller_disappearance_does_not_delay_or_rollback_healthy_host(self):
        self.host.activate()
        self.assertFalse((self.state / "commit.json").exists())
        self.assertEqual("committed", self.host.evidence["phase"])
        self.assertEqual("host-verified", self.host.evidence["controllerSmoke"])
        self.assertEqual("host", self.host.evidence["acceptanceAuthority"])
        restarted = FakeHost(self.root)
        restarted.finalize()
        self.assertEqual([], restarted.calls)

    def test_wrong_process_identity_blocks_commit(self):
        self.host.served_revision = PREVIOUS
        with self.assertRaisesRegex(RuntimeError, "wrong revision"):
            self.host.activate()
        self.assertNotEqual("committed", self.host.evidence["phase"])

    def test_server_exit_between_proxy_smoke_and_final_identity_blocks_commit(self):
        original = self.host.verify_image
        def verify(*args):
            result = original(*args)
            if len([c for c in self.host.calls if c[0] == "identity"]) == 2:
                result["State"]["Running"] = False
            return result
        self.host.verify_image = verify
        with self.assertRaisesRegex(RuntimeError, "server failed during acceptance"):
            self.host.activate()
        self.assertNotEqual("committed", self.host.evidence["phase"])

    def test_wrong_installer_identity_blocks_commit(self):
        self.host.release_tag = "v9.9.9"
        with self.assertRaisesRegex(RuntimeError, "installer release identity"):
            self.host.activate()
        self.assertNotEqual("committed", self.host.evidence["phase"])

    def test_wrong_proxy_identity_blocks_commit(self):
        self.host.proxy_revision = PREVIOUS
        with self.assertRaisesRegex(RuntimeError, "wrong revision"):
            self.host.activate()
        self.assertNotEqual("committed", self.host.evidence["phase"])

    def test_changed_lock_rejects_otherwise_healthy_candidate(self):
        (self.root.parent / ".deploy-lock" / "owner").write_text("0" * 32)
        with self.assertRaisesRegex(RuntimeError, "ownership changed"):
            self.host.activate()
        self.assertNotEqual("committed", self.host.evidence["phase"])

    def test_migration_deadline_does_not_activate_target(self):
        self.host.helper_running = True
        with self.assertRaisesRegex(RuntimeError, "migration deadline"):
            self.host.migrate()
        self.assertFalse(any(k == "docker" and "up" in v for k, v in self.host.calls))
        self.host.finalize()
        self.assertEqual(self.host.evidence["cleanup"], "pass")

    def test_nonzero_migration_exit_is_failure(self):
        self.host.helper_exit = 9
        with self.assertRaisesRegex(RuntimeError, "helper failed"):
            self.host.migrate()

    def test_migration_acceptance_uses_only_ledger_catalog_and_current_stats_row(self):
        self.host.verify_migration()
        self.assertEqual(self.host.evidence["migrationVerification"], "pass")
        queries = [q for k, q in self.host.calls if k == "sql"]
        self.assertEqual(len(queries), 3)
        self.assertIn("schema_migrations", queries[0])
        self.assertIn("pg_get_indexdef", queries[1])
        self.assertIn("LIMIT 1 FOR UPDATE", queries[2])
        for table in ("evidence_agg", "compatibility_snapshots", "samples", "receipts"):
            self.assertNotRegex(" ".join(queries), r"(?i)FROM\s+" + table + r"\b")
        self.host.query = lambda _: {"version": "0035_previous.sql", "count": 36}
        with self.assertRaisesRegex(RuntimeError, "ledger does not match"):
            self.host.verify_migration()

    def test_migration_0037_acceptance_verifies_all_six_indexes_and_count_38(self):
        self.host.config["expectedMigration"] = "0037_slow_query_indexes.sql"
        self.host.verify_migration()
        self.assertEqual(self.host.evidence["migrationVerification"], "pass")
        queries = [q for k, q in self.host.calls if k == "sql"]
        index_query = [q for q in queries if "pg_get_indexdef" in q][0]
        self.assertIn("failure_clusters_pkg_count_idx", index_query)
        self.assertIn("samples_live_created_id_idx", index_query)

    def test_migration_0037_rejects_missing_or_invalid_index(self):
        self.host.config["expectedMigration"] = "0037_slow_query_indexes.sql"
        self.host.index_fault = "missing"
        with self.assertRaisesRegex(RuntimeError, "required builder indexes are missing"):
            self.host.verify_migration()
        self.host.index_fault = "wrong"
        with self.assertRaisesRegex(RuntimeError, "builder index is not valid, ready and exact"):
            self.host.verify_migration()

    def test_migration_0037_rejects_wrong_ledger_count(self):
        self.host.config["expectedMigration"] = "0037_slow_query_indexes.sql"
        original_query = self.host.query
        self.host.query = lambda sql: {"version": "0037_slow_query_indexes.sql", "count": 37} if "max(version)" in sql else original_query(sql)
        with self.assertRaisesRegex(RuntimeError, "ledger does not match the target"):
            self.host.verify_migration()

    def test_unreviewed_migration_rejected_at_startup(self):
        (self.state / "config.json").write_text(json.dumps({
            "targetSha": TARGET, "previousSha": PREVIOUS, "operationalSha": CONTROL,
            "imageDigest": IMAGE, "previousImageDigest": "sha256:" + "f" * 64,
            "expectedMigration": "0038_unknown.sql", "migrationTimeoutSeconds": 60,
            "expectedReleaseTag": RELEASE}))
        with self.assertRaisesRegex(ValueError, "offline migration supports only reviewed migrations"):
            FakeHost(self.root)

    def test_migration_0038_startup_and_exact_index_acceptance(self):
        config = json.loads((self.state / "config.json").read_text())
        config["expectedMigration"] = "0038_active_installations.sql"
        (self.state / "config.json").write_text(json.dumps(config))
        host = FakeHost(self.root)
        host.verify_migration()
        self.assertEqual(host.evidence["migrationLedger"], {
            "version": "0038_active_installations.sql", "count": 39})
        names = {row["name"] for row in host.evidence["indexes"]}
        self.assertTrue({"active_installations_pkey", "active_installations_token_key",
                         "active_installations_count_idx", "active_installations_prune_idx"} <= names)
        for fault in ("missing", "wrong"):
            host.index_fault = fault
            with self.assertRaises(RuntimeError):
                host.verify_migration()

    def test_latest_payload_migration_has_reviewed_host_acceptance(self):
        files = sorted((Path(__file__).resolve().parents[2] / "internal/serverstore/migrations").glob("*.sql"))
        self.assertIn(files[-1].name, migration.REVIEWED_MIGRATIONS)
        self.assertEqual(len(files), migration.REVIEWED_MIGRATIONS[files[-1].name]["count"])

    def test_migration_0040_checks_analytics_indexes_before_acceptance(self):
        config = json.loads((self.state / "config.json").read_text())
        config["expectedMigration"] = "0040_anonymous_analytics.sql"
        (self.state / "config.json").write_text(json.dumps(config))
        host = FakeHost(self.root)
        host.verify_migration()
        self.assertEqual({"version": "0040_anonymous_analytics.sql", "count": 41}, host.evidence["migrationLedger"])
        names = {row["name"] for row in host.evidence["indexes"]}
        self.assertTrue({"anonymous_clients_pkey", "anonymous_clients_first_seen_idx",
                         "anonymous_clients_last_seen_idx", "anonymous_client_days_pkey",
                         "anonymous_client_days_client_idx", "anonymous_analytics_collection_pkey"} <= names)
        for fault in ("missing", "wrong", "invalid", "not-ready"):
            host.index_fault = fault
            with self.assertRaises(RuntimeError):
                host.verify_migration()

    def test_migration_0041_requires_exact_credential_adoption_columns(self):
        config = json.loads((self.state / "config.json").read_text())
        config["expectedMigration"] = "0041_anonymous_credential_adoption.sql"
        (self.state / "config.json").write_text(json.dumps(config))
        host = FakeHost(self.root)
        host.verify_migration()
        self.assertEqual({"version": "0041_anonymous_credential_adoption.sql", "count": 42},
                         host.evidence["migrationLedger"])
        self.assertEqual(host.credential_adoption_columns,
                         host.evidence["credentialAdoptionColumns"])
        for columns in (None, [], host.credential_adoption_columns[:-1], [
                {**column, "default": "1"} if column["name"] == "credential_present_count" else column
                for column in host.credential_adoption_columns]):
            with self.subTest(columns=columns):
                host.credential_adoption_columns = columns
                host.barrier_rearmed = False
                with self.assertRaisesRegex(RuntimeError, "credential adoption columns"):
                    host.verify_migration()
                self.assertFalse(host.barrier_rearmed)

    def test_migration_0042_accepts_exact_page_index_and_count_43(self):
        config = json.loads((self.state / "config.json").read_text())
        config["expectedMigration"] = "0042_failure_cluster_page_idx.sql"
        (self.state / "config.json").write_text(json.dumps(config))
        host = FakeHost(self.root)
        host.verify_migration()
        self.assertEqual({"version": "0042_failure_cluster_page_idx.sql", "count": 43},
                         host.evidence["migrationLedger"])
        names = {row["name"] for row in host.evidence["indexes"]}
        self.assertIn("failure_clusters_current_page_idx", names)
        definition = next(row["definition"] for row in host.evidence["indexes"]
                          if row["name"] == "failure_clusters_current_page_idx")
        self.assertIn("observation_count DESC", definition)
        self.assertIn("evidence_quality", definition)
        for fault in ("missing", "wrong", "invalid", "not-ready"):
            host.index_fault = fault
            with self.assertRaises(RuntimeError):
                host.verify_migration()

    def test_migration_0039_requires_the_exact_review_note_column(self):
        config = json.loads((self.state / "config.json").read_text())
        config["expectedMigration"] = "0039_report_review_notes.sql"
        (self.state / "config.json").write_text(json.dumps(config))
        host = FakeHost(self.root)
        host.verify_migration()
        self.assertEqual({"version": "0039_report_review_notes.sql", "count": 40}, host.evidence["migrationLedger"])
        self.assertEqual(host.review_note_column, host.evidence["reviewNoteColumn"])
        for column in (None, {}, {"type": "varchar", "nullable": "NO", "default": "''::text"},
                       {"type": "text", "nullable": "YES", "default": "''::text"},
                       {"type": "text", "nullable": "NO", "default": None}):
            with self.subTest(column=column):
                host.review_note_column = column
                host.barrier_rearmed = False
                with self.assertRaisesRegex(RuntimeError, "review note column"):
                    host.verify_migration()
                self.assertFalse(host.barrier_rearmed)

    def test_recovery_script_restores_dist_before_old_container_recreation(self):
        script = Path(__file__).with_name("rollback-server.sh").read_text()
        self.assertLess(script.index("mv /opt/codesamplex/dist.rollback-stage /opt/codesamplex/dist"),
                        script.index("docker compose up -d --no-build --no-deps --force-recreate server"))
        self.assertIn("deadline=$(($(date +%s) + 60))", script)

    def test_supervisor_uses_type_exec_and_a_separate_stop_budget(self):
        script = Path(__file__).with_name("offline-migration.ps1").read_text()
        self.assertIn("--property=Type=exec", script)
        self.assertIn("--property=RuntimeMaxSec=", script)
        # The finalizer must be allowed to finish cleanup and both exact
        # restorations; see the enclosing-ceiling arithmetic test below.
        self.assertIn("--property=TimeoutStopSec=" +
                      str(migration.RECOVERY_STOP_ALLOWANCE_SECONDS), script)
        self.assertIn("ExecStopPost=", script)
        self.assertIn('"rolled-back-degraded"', script)
        self.assertNotIn("prestage-builder-indexes", script)
        self.assertEqual(2, script.count('"0042_failure_cluster_page_idx.sql" { 43 }'))


    def test_quiescence_is_proved_without_a_full_table_baseline(self):
        self.host.backend_present = False
        self.host.helper_present = False
        self.host.inspect = lambda _: {"State": {"StartedAt": "2026-09-09T01:00:00Z"}, "NetworkSettings": {"Networks": {}}}
        self.host.after["samples"] = 19
        self.host.stop_builders()
        self.assertFalse(any(k == "sql" for k, _ in self.host.calls))
        self.assertEqual("pass", self.host.evidence["quiescence"])

    def test_quiescence_terminates_only_the_stopped_server_backend(self):
        self.host.backend_present = False
        self.host.helper_present = False
        self.host.server_backend_present = True
        self.host.inspect = lambda _: {"State": {"StartedAt": "2026-09-09T01:00:00Z"},
                                       "NetworkSettings": {"Networks": {
                                           "default": {"IPAddress": "172.20.0.4"}}}}
        self.host.stop_builders()
        signals = [sql for kind, sql in self.host.calls if kind == "sql" and "_backend" in sql]
        self.assertTrue(any("pg_cancel_backend" in sql and "pid=2718" in sql for sql in signals))
        self.assertTrue(any("pg_terminate_backend" in sql and "pid=2718" in sql for sql in signals))
        self.assertEqual("pass", self.host.evidence["serverBackendCleanup"])
        self.assertEqual("pass", self.host.evidence["quiescence"])

    def test_quiescence_still_refuses_a_foreign_database_client(self):
        self.host.server_backend_present = False
        self.host.inspect = lambda _: {"State": {"StartedAt": "2026-09-09T01:00:00Z"},
                                       "NetworkSettings": {"Networks": {}}}
        with self.assertRaisesRegex(RuntimeError, "unowned database clients"):
            self.host.stop_builders()

    def test_surviving_unowned_client_cannot_pass_empty_owned_cleanup(self):
        self.host.helper_present = False
        self.host.backend_present = False
        self.host.evidence.update({
            "quiescence": "pass",
            "originalServerNetwork": {
                "addresses": ["172.20.0.4"],
                "startedAt": "2026-09-09T01:00:00Z",
            },
        })
        self.host.clients = lambda owned_only=False: [] if owned_only else [{
            "pid": 42,
            "backendStart": "2026-09-09 01:00:00+00",
            "queryStart": "2026-09-09 01:00:01+00",
            "applicationName": "",
            "userName": "csx",
            "clientAddress": "172.20.0.5",
            "queryHash": "1" * 32,
        }]
        with self.assertRaisesRegex(RuntimeError, "unowned database clients"):
            self.host.cleanup_helper()
        self.assertFalse(any(kind == "sql" and "_backend" in sql
                             for kind, sql in self.host.calls))
        self.assertEqual(42, self.host.evidence["unownedClientsAtCleanup"][0]["pid"])
        self.assertNotIn("query", self.host.evidence["unownedClientsAtCleanup"][0])

    def test_unowned_client_does_not_suppress_recovery_rollback(self):
        row = {
            "pid": 42,
            "backendStart": "2026-09-09 00:30:00+00",
            "queryStart": "2026-09-09 01:00:01+00",
            "applicationName": "pg_dump",
            "userName": "csx",
            "clientAddress": None,
            "queryHash": "1" * 32,
        }
        self.host.helper_present = False
        self.host.backend_present = False
        self.host.evidence.update({
            "phase": "migrating",
            "quiescence": "pass",
            "originalServerNetwork": {
                "addresses": ["172.20.0.4"],
                "startedAt": "2026-09-09T01:00:00Z",
            },
        })
        self.host.clients = lambda owned_only=False: [] if owned_only else [row]

        self.host.finalize()

        commands = [Path(value[1]).name for kind, value in self.host.calls
                    if kind == "command" and value[0] == "sh"]
        self.assertEqual(["rollback-server.sh", "rollback-caddy.sh"], commands)
        self.assertEqual("rolled-back-degraded", self.host.evidence["phase"])
        self.assertEqual("degraded", self.host.evidence["cleanup"])
        self.assertEqual([row], self.host.evidence["unownedClientsAtCleanup"])
        signals = [sql for kind, sql in self.host.calls if kind == "sql" and "_backend" in sql]
        self.assertFalse(any("pid=42" in sql for sql in signals))

    def test_helper_cleanup_terminates_late_original_server_backend(self):
        self.host.helper_present = False
        self.host.backend_present = False
        self.host.server_backend_present = True
        self.host.evidence.update({
            "quiescence": "pass",
            "originalServerNetwork": {
                "addresses": ["172.20.0.4"],
                "startedAt": "2026-09-09T01:00:00Z",
            },
        })
        server_backend = {
            "pid": 2718,
            "backendStart": "2026-09-09 01:00:00+00",
            "queryStart": "2026-09-09 01:00:01+00",
            "applicationName": "",
            "userName": "csx",
            "clientAddress": "172.20.0.4",
            "queryHash": "0" * 32,
        }
        self.host.clients = lambda owned_only=False: (
            [] if owned_only or not self.host.server_backend_present else [server_backend]
        )

        self.host.cleanup_helper()

        signals = [sql for kind, sql in self.host.calls if kind == "sql" and "_backend" in sql]
        self.assertTrue(any("pg_cancel_backend" in sql and "pid=2718" in sql for sql in signals))
        self.assertTrue(any("pg_terminate_backend" in sql and "pid=2718" in sql for sql in signals))
        self.assertEqual("pass", self.host.evidence["cleanup"])

    def test_terminate_grace_observes_again_after_one_slow_poll(self):
        row = {
            "pid": 1729,
            "backendStart": "2026-09-09 01:00:00+00",
            "queryStart": "2026-09-09 01:00:01+00",
            "applicationName": self.host.application,
            "queryHash": "f" * 32,
        }
        now = [0]
        state = {"cancelled": False, "terminated": False, "postTerminate": 0}
        original_query = self.host.query

        def query(sql):
            if "pg_cancel_backend" in sql:
                state["cancelled"] = True
            if "pg_terminate_backend" in sql:
                state["terminated"] = True
            return original_query(sql)

        def clients(owned_only=False):
            if not owned_only:
                return []
            if state["terminated"]:
                state["postTerminate"] += 1
                now[0] += 11
                return [row] if state["postTerminate"] == 1 else []
            if state["cancelled"]:
                now[0] = 6
            return [row]

        patch.object(migration.time, "monotonic", lambda: now[0]).start()
        self.host.helper_present = False
        self.host.terminate_clears = False
        self.host.query = query
        self.host.clients = clients

        self.host.cleanup_helper()

        self.assertEqual(2, state["postTerminate"])
        self.assertEqual("pass", self.host.evidence["cleanup"])

    def test_server_backend_cleanup_precedes_helper_and_rollback(self):
        self.host.server_present = True
        self.host.server_backend_present = True
        self.host.finalize()
        calls = self.host.calls
        stop = next(i for i, c in enumerate(calls) if c == ("docker", ("stop", "--time", "10", "codesamplex-server-1")))
        signal = next(i for i, c in enumerate(calls) if c[0] == "sql" and "pg_terminate_backend" in c[1] and "pid=2718" in c[1])
        rollback = next(i for i, c in enumerate(calls) if c[0] == "command" and c[1][0] == "sh")
        self.assertLess(stop, signal)
        self.assertLess(signal, rollback)
        self.assertEqual("pass", self.host.evidence["rollbackServerCleanup"])
        self.assertEqual("rolled-back", self.host.evidence["phase"])

    def test_unknown_server_image_blocks_cleanup_mutation(self):
        self.host.server_present = True
        self.host.server_image = "sha256:" + "0" * 64
        with self.assertRaisesRegex(RuntimeError, "server image changed"):
            self.host.finalize()
        self.assertFalse(any(c[0] == "docker" and c[1][0] == "stop" for c in self.host.calls))

    def test_uri_identity_replaces_inherited_application_name_without_changing_credentials(self):
        source = "postgres://csx:p%40ss@db:5432/csx?sslmode=disable&application_name=foreign&application_name=other"
        result = migration.owned_dsn(source, "csx-migrate-" + OWNER)
        uri = migration.urlsplit(result)
        self.assertEqual("csx:p%40ss@db:5432", uri.netloc)
        self.assertEqual([("sslmode", "disable"), ("application_name", "csx-migrate-" + OWNER)],
                         migration.parse_qsl(uri.query))
        for unsupported in ("host=db user=csx", "postgres://db/csx#fragment", "postgres://db/csx\nx"):
            with self.assertRaises(RuntimeError):
                migration.owned_dsn(unsupported, self.host.application)

    def test_helper_identity_override_failure_never_activates(self):
        self.host.helper_environment["CSX_DSN"] = "postgres://db/other"
        with self.assertRaisesRegex(RuntimeError, "identity override"):
            self.host.migrate()
        self.assertFalse(any(c[0] == "docker" and c[1][:2] == ("compose", "up") for c in self.host.calls))

    def test_post_migration_ledger_is_recorded_before_cleanup_refusal(self):
        self.host.backend_present = False
        self.host.migrate()
        expected = self.target_ledger()
        self.assertEqual(expected, self.host.evidence["migrationLedgerAfter"])
        self.host.helper_present = False
        self.host.evidence["quiescence"] = "pass"
        self.host.clients = lambda owned_only=False: [] if owned_only else [{"pid": 42}]
        with self.assertRaisesRegex(RuntimeError, "unowned database clients"):
            self.host.cleanup_helper()
        self.assertEqual(expected, self.host.evidence["migrationLedgerAfter"])

    def test_rollback_server_backends_dedupe_by_backend_identity(self):
        network = {"addresses": ["172.20.0.4"], "startedAt": "2026-09-09T01:00:00Z"}
        observations = iter([
            [{"pid": 2718, "backendStart": "2026-09-09 01:00:00+00",
              "queryStart": "2026-09-09 01:00:01+00", "applicationName": "",
              "userName": "csx", "clientAddress": "172.20.0.4", "queryHash": "1" * 32}],
            [{"pid": 2718, "backendStart": "2026-09-09 01:00:00+00",
              "queryStart": "2026-09-09 01:00:02+00", "applicationName": "",
              "userName": "csx", "clientAddress": "172.20.0.4", "queryHash": "2" * 32}],
        ])
        original_query = self.host.query
        self.host.query = lambda sql: next(observations) if "client_addr=ANY" in sql else original_query(sql)

        self.host.remember_server_backends([network])
        latest = self.host.remember_server_backends([network])[0]

        self.assertEqual(1, len(self.host.evidence["rollbackServerBackends"]))
        self.assertEqual(latest, self.host.evidence["rollbackServerBackends"][0])


    def target_ledger(self):
        migrationfile = self.host.config["expectedMigration"]
        return {"version": migrationfile, "count": migration.REVIEWED_MIGRATIONS[migrationfile]["count"]}

    def test_ledger_baseline_is_recorded_once_and_never_overwritten(self):
        self.host.ledger_version = "0035_previous.sql"
        self.host.record_ledger_baseline()
        first = self.host.evidence["migrationLedgerBefore"]
        self.assertEqual("0035_previous.sql", first["version"])
        # A restarted supervisor re-runs preflight after its own migration has
        # already moved the head; the first observation is the deployment's.
        self.host.ledger_version = None
        self.host.record_ledger_baseline()
        self.assertEqual(first, self.host.evidence["migrationLedgerBefore"])

    def test_noop_deployment_leaves_an_unarmed_barrier_unarmed(self):
        self.host.evidence["migrationLedgerBefore"] = self.target_ledger()
        self.host.verify_migration()
        self.assertFalse(self.host.barrier_rearmed)
        self.assertFalse(self.host.barrier_armed)
        self.assertNotIn("repairBarrierRearmed", self.host.evidence)
        self.assertEqual({"count": 1, "day": "2026-09-09", "armed": False},
                         self.host.evidence["repairBarrierObserved"])

    def test_noop_deployment_leaves_an_armed_barrier_armed(self):
        self.host.evidence["migrationLedgerBefore"] = self.target_ledger()
        self.host.barrier_armed = True
        self.host.verify_migration()
        self.assertFalse(self.host.barrier_rearmed)
        self.assertTrue(self.host.barrier_armed)
        self.assertEqual({"count": 1, "day": "2026-09-09", "armed": True},
                         self.host.evidence["repairBarrierObserved"])

    def test_applied_migration_rearms_the_barrier_without_corpus_scan(self):
        for armed in (False, True):
            with self.subTest(armed=armed):
                self.setUp()
                self.host.evidence["migrationLedgerBefore"] = {"version": "0035_previous.sql", "count": 36}
                self.host.barrier_armed = armed
                self.host.verify_migration()
                self.assertTrue(self.host.barrier_rearmed)
                self.assertTrue(self.host.barrier_armed)
                self.assertEqual({"count": 1, "day": "2026-09-09", "armed": True},
                                 self.host.evidence["repairBarrierRearmed"])
                self.assertNotIn("repairBarrierObserved", self.host.evidence)
                self.assertFalse(any(c[0] == "sql" and ("FROM samples" in c[1] or "FROM receipts" in c[1])
                                     for c in self.host.calls))

    def test_index_only_migration_does_not_rearm_the_barrier(self):
        self.host.config["expectedMigration"] = "0042_failure_cluster_page_idx.sql"
        self.host.evidence["migrationLedgerBefore"] = {
            "version": "0041_anonymous_credential_adoption.sql", "count": 42}
        self.host.verify_migration()
        self.assertFalse(self.host.barrier_rearmed)
        self.assertFalse(self.host.barrier_armed)
        self.assertNotIn("repairBarrierRearmed", self.host.evidence)
        self.assertEqual({"count": 1, "day": "2026-09-09", "armed": False},
                         self.host.evidence["repairBarrierObserved"])

    def test_index_only_migration_never_clears_an_existing_barrier(self):
        self.host.config["expectedMigration"] = "0042_failure_cluster_page_idx.sql"
        self.host.evidence["migrationLedgerBefore"] = {
            "version": "0041_anonymous_credential_adoption.sql", "count": 42}
        self.host.barrier_armed = True
        self.host.verify_migration()
        self.assertFalse(self.host.barrier_rearmed)
        self.assertTrue(self.host.barrier_armed)
        self.assertEqual({"count": 1, "day": "2026-09-09", "armed": True},
                         self.host.evidence["repairBarrierObserved"])

    def test_migration_range_rejects_gapped_or_duplicate_reviewed_counts(self):
        before = {"version": "0040_anonymous_analytics.sql", "count": 41}
        target = {"version": "0042_failure_cluster_page_idx.sql", "count": 43}
        gapped = dict(migration.REVIEWED_MIGRATIONS)
        del gapped["0041_anonymous_credential_adoption.sql"]
        duplicate = dict(migration.REVIEWED_MIGRATIONS)
        duplicate["0041_duplicate.sql"] = {
            "count": 42, "builderRepairRequired": False, "indexes": {}}
        for reviewed in (gapped, duplicate):
            with self.subTest(reviewed=sorted(reviewed)):
                with patch.object(migration, "REVIEWED_MIGRATIONS", reviewed):
                    self.assertTrue(
                        migration.migration_range_requires_builder_repair(before, target))

    def test_migration_range_rejects_untrusted_baselines(self):
        target = {"version": "0042_failure_cluster_page_idx.sql", "count": 43}
        baselines = (
            None,
            ["0041_anonymous_credential_adoption.sql", 42],
            {"version": "0043_future.sql", "count": 44},
            {"version": "0041_wrong_name.sql", "count": 42},
        )
        for before in baselines:
            with self.subTest(before=before):
                self.assertTrue(
                    migration.migration_range_requires_builder_repair(before, target))

    def test_migration_range_accepts_reviewed_index_only_0042(self):
        self.assertFalse(migration.migration_range_requires_builder_repair(
            {"version": "0041_anonymous_credential_adoption.sql", "count": 42},
            {"version": "0042_failure_cluster_page_idx.sql", "count": 43}))

    def test_ledger_jump_crossing_builder_projection_migration_rearms(self):
        self.host.config["expectedMigration"] = "0042_failure_cluster_page_idx.sql"
        self.host.evidence["migrationLedgerBefore"] = {
            "version": "0035_previous.sql", "count": 36}
        self.host.verify_migration()
        self.assertTrue(self.host.barrier_rearmed)
        self.assertTrue(self.host.barrier_armed)
        self.assertEqual({"count": 1, "day": "2026-09-09", "armed": True},
                         self.host.evidence["repairBarrierRearmed"])

    def test_contradictory_prior_ledger_rearms_fail_closed(self):
        self.host.config["expectedMigration"] = "0042_failure_cluster_page_idx.sql"
        self.host.evidence["migrationLedgerBefore"] = {
            "version": "0041_unreviewed_name.sql", "count": 42}
        self.host.verify_migration()
        self.assertTrue(self.host.barrier_rearmed)
        self.assertTrue(self.host.barrier_armed)

    def test_every_reviewed_migration_classifies_builder_repair_explicitly(self):
        self.assertEqual(
            ["0036_builder_projections.sql"],
            [name for name, target in migration.REVIEWED_MIGRATIONS.items()
             if target["builderRepairRequired"]])

    def test_unknown_prior_ledger_rearms_the_barrier(self):
        self.assertNotIn("migrationLedgerBefore", self.host.evidence)
        self.host.verify_migration()
        self.assertTrue(self.host.barrier_rearmed)
        self.assertEqual({"count": 1, "day": "2026-09-09", "armed": True},
                         self.host.evidence["repairBarrierRearmed"])

    def test_missing_or_ambiguous_current_stats_row_blocks_activation(self):
        for before, count in ((None, 0), (None, 2), (self.target_ledger(), 0), (self.target_ledger(), 2)):
            with self.subTest(before=before, count=count):
                self.setUp()
                if before is not None:
                    self.host.evidence["migrationLedgerBefore"] = before
                self.host.barrier_count = count
                with self.assertRaisesRegex(RuntimeError, "exactly one current stats row"):
                    self.host.verify_migration()

    def test_rearm_that_does_not_take_blocks_activation(self):
        self.host.barrier_rearm_sticks = False
        with self.assertRaisesRegex(RuntimeError, "exactly one current stats row"):
            self.host.verify_migration()

    def test_missing_wrong_or_not_ready_index_blocks_migration_acceptance(self):
        for fault in ("missing", "wrong", "invalid", "not-ready"):
            with self.subTest(fault=fault):
                self.host.index_fault = fault
                with self.assertRaisesRegex(RuntimeError, "indexes are missing|valid, ready and exact"):
                    self.host.verify_migration()
                self.assertFalse(self.host.barrier_rearmed)
                self.assertFalse(self.host.barrier_armed)

    def test_host_owns_only_required_stack_mutations_before_commit(self):
        self.host.activate()
        self.assertEqual("committed", self.host.evidence["phase"])
        up = [c[1] for c in self.host.calls if c[0] == "docker" and c[1][:2] == ("compose", "up")]
        self.assertEqual([
            ("compose", "up", "-d", "--no-build", "--no-deps", "--force-recreate", "server"),
            ("compose", "up", "-d", "--no-build", "--no-deps", "--force-recreate", "caddy"),
        ], up)
        self.assertFalse(any("reload" in c[1] for c in self.host.calls))
        requests = [c[1] for c in self.host.calls if c[0] == "command" and c[1][0] == "curl" and not c[1][-1].endswith("/healthz")]
        self.assertEqual(["https://codesamplex.dev/features", "https://codesamplex.dev/version"],
                         [r[-1] for r in requests])
        self.assertTrue(all("--resolve" in r and "--retry" not in r for r in requests))
        self.assertNotIn("safe-log-smoke.sh", Path(__file__).with_name("offline-migration.py").read_text())

    def test_activation_evidence_contains_one_real_deadline_and_phase_duration(self):
        self.host.activate()
        start = migration.datetime.datetime.fromisoformat(self.host.evidence["activationStartedAt"])
        deadline = migration.datetime.datetime.fromisoformat(self.host.evidence["activationDeadlineAt"])
        self.assertEqual(180, (deadline - start).total_seconds())
        self.assertEqual(180, self.host.evidence["activationBudgetSeconds"])
        self.assertEqual("pass", self.host.evidence["phaseTimings"]["activation"]["outcome"])
        self.assertLess(self.host.evidence["activationElapsedSeconds"], 180)
        self.assertEqual(TARGET, self.host.evidence["servedRevision"])
        self.assertEqual(RELEASE, self.host.evidence["releaseTag"])

    def test_final_identity_cannot_commit_after_activation_deadline(self):
        now = [0]
        original = self.host.verify_image
        def verify(*args):
            result = original(*args)
            if len([c for c in self.host.calls if c[0] == "identity"]) == 2:
                now[0] = 181
            return result
        self.host.verify_image = verify
        with patch.object(migration.time, "monotonic", lambda: now[0]):
            with self.assertRaisesRegex(RuntimeError, "activation deadline"):
                self.host.activate()
        self.assertNotEqual("committed", self.host.evidence["phase"])
        self.assertEqual("failure", self.host.evidence["phaseTimings"]["activation"]["outcome"])

    def test_readiness_failure_is_bounded_and_does_not_start_caddy(self):
        now = [0]
        self.host.health = "starting"
        def sleep(seconds):
            now[0] += seconds
        with patch.object(migration.time, "monotonic", lambda: now[0]), patch.object(migration.time, "sleep", sleep):
            with self.assertRaisesRegex(RuntimeError, "health deadline"):
                self.host.activate()
        self.assertEqual(45, now[0])
        self.assertNotEqual("committed", self.host.evidence["phase"])
        self.assertFalse(any(c[0] == "docker" and c[1][-1] == "caddy" for c in self.host.calls))

    def test_cleanup_preserves_existing_deadline_on_success_and_failure(self):
        for foreign in (False, True):
            with self.subTest(foreign=foreign):
                self.host.foreign_helper = foreign
                self.host.helper_present = True
                self.host.operation_deadline = 99999
                if foreign:
                    with self.assertRaisesRegex(RuntimeError, "ownership mismatch"):
                        self.host.cleanup_helper()
                else:
                    self.host.cleanup_helper()
                self.assertEqual(99999, self.host.operation_deadline)

    def test_migration_duration_does_not_consume_activation_budget(self):
        now = [0]
        self.host.backend_present = False
        with patch.object(migration.time, "monotonic", lambda: now[0]):
            self.host.migrate()
            now[0] = 1800
            self.host.activate()
        self.assertEqual(0, self.host.evidence["activationElapsedSeconds"])
        self.assertEqual(180, self.host.evidence["activationBudgetSeconds"])
        self.assertIsNone(self.host.operation_deadline)

    def test_slow_sequential_activation_commands_share_one_180_second_deadline(self):
        now = [0]
        timeouts = []
        container = {"Image": IMAGE, "Config": {"Labels": {"org.opencontainers.image.revision": TARGET}},
                     "State": {"StartedAt": "2026-09-09T01:00:00Z", "Running": True}}
        class Process:
            pid = 42042
            returncode = 0
            def __init__(process, args, **kwargs):
                process.args = args
                if args[0] == "curl":
                    path = args[-1]
                    process.duration = 1 if path.endswith("/healthz") else 9
                    body = ("ok" if path.endswith("/healthz") else
                            json.dumps({"revision": TARGET}) if path.endswith("/version") else
                            '<link rel="canonical" href="https://codesamplex.dev/features">')
                    process.output = body + "\n200"
                elif args[1:3] == ["compose", "up"]:
                    process.duration = 55 if args[-1] == "server" else 40
                    process.output = ""
                elif args[1] in ("inspect", "image"):
                    process.duration = 15
                    process.output = json.dumps([container])
                else:
                    process.duration = 4
                    process.output = ("ok" if args[-1].endswith("/healthz") else
                                      json.dumps({"revision": TARGET}) if args[-1].endswith("/version") else RELEASE)
            def communicate(process, timeout):
                timeouts.append((process.args, timeout))
                now[0] += min(process.duration, timeout)
                if process.duration > timeout:
                    raise subprocess.TimeoutExpired(process.args, timeout)
                return process.output, ""
            def wait(process, timeout):
                return 0
        for name in ("command", "docker", "inspect", "verify_image"):
            setattr(self.host, name, getattr(migration.Host, name).__get__(self.host))
        with patch.object(migration.time, "monotonic", lambda: now[0]), \
             patch.object(migration.subprocess, "Popen", Process), \
             patch.object(migration.signal, "SIGKILL", 9, create=True), \
             patch.object(migration.os, "killpg", create=True) as killed:
            with self.assertRaises(subprocess.TimeoutExpired):
                self.host.activate()
        self.assertEqual(180, now[0])
        self.assertEqual(9, timeouts[-1][1])
        self.assertEqual(["docker", "image", "inspect", IMAGE], timeouts[-1][0])
        killed.assert_called_once_with(42042, 9)
        self.assertNotEqual("committed", self.host.evidence["phase"])
        self.assertEqual("failure", self.host.evidence["phaseTimings"]["activation"]["outcome"])
        self.assertIsNone(self.host.operation_deadline)

    def test_proxy_listener_warmup_precedes_the_two_single_representatives(self):
        now = [0]
        original = self.host.command
        readiness = []
        def command(args, seconds=30, check=True, environment=None):
            if args[0] == "curl" and args[-1].endswith("/healthz"):
                readiness.append(now[0])
                if len(readiness) < 3:
                    return subprocess.CompletedProcess(args, 7, "", "")
            return original(args, seconds, check, environment)
        self.host.command = command
        with patch.object(migration.time, "monotonic", lambda: now[0]), \
             patch.object(migration.time, "sleep", lambda seconds: now.__setitem__(0, now[0] + seconds)):
            self.host.activate()
        self.assertEqual([0, 1, 2], readiness)
        self.assertEqual("committed", self.host.evidence["phase"])
        self.assertEqual(2, self.host.evidence["phaseTimings"]["proxyReadiness"]["elapsedSeconds"])

    def test_proxy_unavailable_exhausts_only_its_60_second_readiness_budget(self):
        now = [0]
        self.host.proxy_status = "503"
        with patch.object(migration.time, "monotonic", lambda: now[0]), \
             patch.object(migration.time, "sleep", lambda seconds: now.__setitem__(0, now[0] + seconds)):
            with self.assertRaisesRegex(RuntimeError, "proxy health deadline"):
                self.host.activate()
        self.assertEqual(60, now[0])
        self.assertNotEqual("committed", self.host.evidence["phase"])
        self.assertFalse(any(c[0] == "command" and c[1][-1].endswith("/features") for c in self.host.calls))

    def test_signal_after_durable_commit_cannot_overwrite_success(self):
        committed = []
        def run():
            self.host.activate()
            committed.append(self.host.evidence_file.read_bytes())
            raise RuntimeError("host supervisor interrupted")
        self.host.run = run
        with patch.object(migration, "Host", lambda _: self.host), \
             patch.object(migration, "__file__", str(self.state / "offline-migration.py")), \
             patch.object(migration.signal, "signal"):
            result = migration.main(["offline-migration.py", "run", OWNER])
        self.assertEqual(0, result)
        self.assertEqual(committed[0], self.host.evidence_file.read_bytes())
        self.assertEqual("success", json.loads(committed[0])["conclusion"])

    def test_stopped_service_rollback_does_not_start_or_stop_service(self):
        for service in ("server", "caddy"):
            script = Path(__file__).with_name("rollback-" + service + ".sh").read_text()
            self.assertIn("docker compose up --no-start --no-build --no-deps --force-recreate " + service, script)
            self.assertNotIn("docker compose stop " + service, script)


    def test_dist_restoration_requires_its_marker_and_previous_generation(self):
        shell = shutil.which("sh")
        if not shell:
            self.skipTest("POSIX shell unavailable")
        source = Path(__file__).with_name("rollback-server.sh").read_text()
        guard = source[:source.index("if docker container inspect")]
        guard = guard.replace("cd /opt/codesamplex/deploy", 'cd "$CSX_TEST_DIR"')
        guard = guard.replace("/opt/codesamplex/dist.previous", '"$CSX_TEST_PREVIOUS"')
        guard = guard.replace("__CSX_RESTORE_DIST__", "1")
        for name in ("docker-compose.yml.rollback-absent", ".env.rollback-absent",
                     "server-container.rollback-absent", "server-latest.rollback-absent"):
            (self.root / name).touch()
        previous = self.root.parent / "previous"
        marker = self.root / "dist.rollback-promoted"
        env = os.environ.copy()
        env.update(CSX_TEST_DIR=self.root.as_posix(), CSX_TEST_PREVIOUS=previous.as_posix())
        for name, has_marker, has_previous in (("missing-marker", False, True),
                                              ("missing-generation", True, False),
                                              ("complete", True, True)):
            with self.subTest(case=name):
                if has_marker: marker.touch()
                elif marker.exists(): marker.unlink()
                if has_previous: previous.mkdir(exist_ok=True)
                elif previous.exists(): previous.rmdir()
                result = subprocess.run([shell, "-c", guard], env=env,
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, name == "complete", result.stderr)


    # Issue 433 measured one bounded `docker compose exec ... psql` round trip
    # at 10.28 seconds on the production host while CPU steal held at 78-81%.
    # recoveryCleanup encloses stop_server_for_rollback and helperCleanup,
    # whose own bounded waits already reserve 85 seconds before a single round
    # trip is paid, so the retired 60-second budget could not complete its own
    # wait sequence: the deadline, not the cleanup proof, decided recovery and
    # a timeout suppressed the rollback that restores service.
    PRESSURE_OBSERVATION_SECONDS = 2.0
    PRESSURE_STOP_SECONDS = 20.0
    # The shipped budget is a quantified minimum, not an estimate: the
    # structural wait ceiling below, plus the round trips this path pays at the
    # singleton measured on the production host under CPU steal.
    MEASURED_ROUND_TRIP_SECONDS = 10.28
    QUANTIFIED_ROUND_TRIPS = 15
    STRUCTURAL_WAIT_CEILING_SECONDS = 85
    RETIRED_CLEANUP_BUDGET_SECONDS = 60
    SUPERSEDED_CLEANUP_BUDGET_SECONDS = 160
    # The heaviest per-round-trip cost at which this bounded path still
    # completes. Above roughly 6.4 seconds the nested 5-second cancel and
    # 10-second terminate windows refuse on their own, whatever budget encloses
    # them, which is why a larger budget cannot buy coverage past that point.
    COMPLETABLE_ROUND_TRIP_SECONDS = 6.0

    def pressured_host(self, observation_seconds=None, stop_seconds=None):
        """A host whose clock advances only for simulated bounded host work.

        Every Docker/psql round trip and every poll sleep moves the fake clock;
        nothing else does, so these assertions are about the shipped deadlines
        and never about how fast this machine runs the fake. Commands clip to
        the enclosing deadline exactly as Host.command does in production.
        """
        observation = (self.PRESSURE_OBSERVATION_SECONDS if observation_seconds is None
                       else observation_seconds)
        stop = self.PRESSURE_STOP_SECONDS if stop_seconds is None else stop_seconds
        evidence = self.state / "evidence.json"
        if evidence.exists():
            evidence.unlink()
        host = FakeHost(self.root)
        host.server_present = True
        host.server_backend_present = True
        host.save(phase="migrating")
        now = [0.0]

        def spend(seconds):
            deadline = host.operation_deadline
            if deadline is not None:
                remaining = deadline - now[0]
                if remaining <= 0:
                    raise RuntimeError("host operation deadline exceeded")
                if seconds > remaining:
                    now[0] = deadline
                    raise subprocess.TimeoutExpired("host operation", remaining)
            now[0] += seconds

        base = {name: getattr(host, name) for name
                in ("command", "docker", "inspect", "clients", "query", "verify_image")}

        def command(args, seconds=30, check=True, environment=None):
            spend(observation)
            return base["command"](args, seconds, check, environment)

        def docker(*args, seconds=30, check=True, environment=None):
            spend(stop if args and args[0] == "stop" else observation)
            return base["docker"](*args, seconds=seconds, check=check, environment=environment)

        def inspect(name):
            spend(observation)
            return base["inspect"](name)

        def clients(owned_only=False):
            spend(observation)
            return base["clients"](owned_only)

        def query(sql):
            spend(observation)
            return base["query"](sql)

        def verify_image(name, image, revision):
            # A real identity proof is `docker inspect` plus `docker image inspect`.
            spend(observation)
            spend(observation)
            return base["verify_image"](name, image, revision)

        host.command, host.docker, host.inspect = command, docker, inspect
        host.clients, host.query, host.verify_image = clients, query, verify_image
        patch.object(migration.time, "monotonic", lambda: now[0]).start()
        patch.object(migration.time, "sleep", lambda seconds: now.__setitem__(0, now[0] + seconds)).start()
        return host, now

    def test_measured_host_pressure_needs_the_shipped_recovery_cleanup_budget(self):
        host, _ = self.pressured_host()
        host.finalize()
        timing = host.evidence["phaseTimings"]["recoveryCleanup"]
        self.assertEqual(migration.RECOVERY_CLEANUP_BUDGET_SECONDS, timing["budgetSeconds"])
        self.assertEqual("pass", timing["outcome"])
        self.assertLessEqual(timing["elapsedSeconds"], migration.RECOVERY_CLEANUP_BUDGET_SECONDS)
        self.assertEqual("rolled-back", host.evidence["phase"])
        self.assertEqual(["rollback-server.sh", "rollback-caddy.sh"],
                         [Path(value[1]).name for kind, value in host.calls
                          if kind == "command" and value[0] == "sh"])
        # The retired 60-second budget cannot survive that same pressure. This
        # is the regression: identical simulated host, different budget.
        retired, _ = self.pressured_host()
        with patch.object(migration, "RECOVERY_CLEANUP_BUDGET_SECONDS", 60):
            with self.assertRaises((RuntimeError, subprocess.TimeoutExpired)):
                retired.finalize()
        self.assertGreater(timing["elapsedSeconds"], 60)
        self.assertFalse(any(kind == "command" and value[0] == "sh"
                             for kind, value in retired.calls))

    def test_cleanup_budget_covers_the_quantified_round_trip_minimum(self):
        # The structural floor is read off the shipped code, not restated: two
        # `docker stop --time 10` caps and three cancel/terminate grace windows.
        bodies = (inspect.getsource(migration.Host.stop_server_for_rollback) +
                  inspect.getsource(migration.Host._cleanup_helper))
        stops = [int(value) for value in
                 re.findall(r'docker\("stop".*?seconds=(\d+)\)', bodies)]
        graces = [int(value) for value in
                  re.findall(r"deadline = time\.monotonic\(\) \+ (\d+)", bodies)]
        self.assertEqual([20, 20], stops)
        self.assertEqual([5, 10, 5, 10, 5, 10], graces)
        self.assertEqual(self.STRUCTURAL_WAIT_CEILING_SECONDS, sum(stops) + sum(graces))
        # 85 + 15 * 10.28 = 239.2. The budget is that minimum, rounded up.
        minimum = (self.STRUCTURAL_WAIT_CEILING_SECONDS +
                   self.QUANTIFIED_ROUND_TRIPS * self.MEASURED_ROUND_TRIP_SECONDS)
        self.assertGreaterEqual(migration.RECOVERY_CLEANUP_BUDGET_SECONDS, minimum)
        self.assertLess(migration.RECOVERY_CLEANUP_BUDGET_SECONDS, minimum + 1)
        # Both earlier budgets sat below it; the retired one sat below the
        # structural floor alone, so it could never complete its own waits.
        self.assertLess(self.RETIRED_CLEANUP_BUDGET_SECONDS,
                        self.STRUCTURAL_WAIT_CEILING_SECONDS)
        self.assertLess(self.SUPERSEDED_CLEANUP_BUDGET_SECONDS, minimum)

    def test_simulated_cleanup_cost_per_round_trip_is_the_documented_one(self):
        # The characterization the operator docs quote, frozen here so the
        # prose cannot drift away from the shipped code. The clock advances
        # only for simulated bounded host work.
        for cost, expected in ((0.0, 50.0), (5.0, 150.0),
                               (self.COMPLETABLE_ROUND_TRIP_SECONDS, 172.0)):
            with self.subTest(roundTripSeconds=cost):
                host, _ = self.pressured_host(observation_seconds=cost)
                host.finalize()
                timing = host.evidence["phaseTimings"]["recoveryCleanup"]
                self.assertEqual("pass", timing["outcome"])
                self.assertEqual(expected, timing["elapsedSeconds"])
                self.assertEqual("rolled-back", host.evidence["phase"])

    def test_completable_round_trip_pressure_needs_more_than_the_superseded_budget(self):
        # At the heaviest round-trip cost this path can still complete, the
        # cleanup needs 172 seconds. The shipped budget absorbs it and the
        # exact rollback runs; the superseded 160 and the retired 60 both let
        # the enclosing deadline - not the cleanup proof - decide recovery, and
        # neither reaches a rollback script at all.
        host, _ = self.pressured_host(
            observation_seconds=self.COMPLETABLE_ROUND_TRIP_SECONDS)
        host.finalize()
        timing = host.evidence["phaseTimings"]["recoveryCleanup"]
        self.assertEqual(migration.RECOVERY_CLEANUP_BUDGET_SECONDS, timing["budgetSeconds"])
        self.assertEqual("pass", timing["outcome"])
        self.assertGreater(timing["elapsedSeconds"], self.SUPERSEDED_CLEANUP_BUDGET_SECONDS)
        self.assertLessEqual(timing["elapsedSeconds"], migration.RECOVERY_CLEANUP_BUDGET_SECONDS)
        self.assertEqual("rolled-back", host.evidence["phase"])
        self.assertEqual(["rollback-server.sh", "rollback-caddy.sh"],
                         [Path(value[1]).name for kind, value in host.calls
                          if kind == "command" and value[0] == "sh"])
        for retired in (self.RETIRED_CLEANUP_BUDGET_SECONDS,
                        self.SUPERSEDED_CLEANUP_BUDGET_SECONDS):
            with self.subTest(budgetSeconds=retired):
                earlier, _ = self.pressured_host(
                    observation_seconds=self.COMPLETABLE_ROUND_TRIP_SECONDS)
                with patch.object(migration, "RECOVERY_CLEANUP_BUDGET_SECONDS", retired):
                    with self.assertRaises((RuntimeError, subprocess.TimeoutExpired)):
                        earlier.finalize()
                self.assertFalse(any(kind == "command" and value[0] == "sh"
                                     for kind, value in earlier.calls))
                self.assertNotIn(earlier.evidence["phase"],
                                 ("rolled-back", "rolled-back-degraded"))

    def test_recovery_cleanup_stays_bounded_and_still_blocks_rollback(self):
        # At the worst observed 10.28 seconds for every round trip, the nested
        # 5-second cancel and 10-second terminate windows refuse on their own;
        # no enclosing budget can buy coverage there. The phase must still
        # refuse inside its own bound and inside the stop allowance, and a
        # refusal is never downgraded into an advisory rollback.
        host, now = self.pressured_host(observation_seconds=10.28)
        with self.assertRaises((RuntimeError, subprocess.TimeoutExpired)):
            host.finalize()
        timing = host.evidence["phaseTimings"]["recoveryCleanup"]
        self.assertEqual(migration.RECOVERY_CLEANUP_BUDGET_SECONDS, timing["budgetSeconds"])
        self.assertEqual("failure", timing["outcome"])
        self.assertLessEqual(timing["elapsedSeconds"], migration.RECOVERY_CLEANUP_BUDGET_SECONDS)
        self.assertLessEqual(now[0], migration.RECOVERY_STOP_ALLOWANCE_SECONDS)
        self.assertFalse(any(kind == "command" and value[0] == "sh"
                             for kind, value in host.calls))
        self.assertNotIn(host.evidence["phase"], ("rolled-back", "rolled-back-degraded"))

    def test_exact_restoration_reserves_are_independent_of_cleanup_spend(self):
        host, _ = self.pressured_host()
        host.finalize()
        timings = host.evidence["phaseTimings"]
        # Cleanup spent more than a whole restoration reserve, yet each
        # restoration still received its own full budget: execute_phase
        # restores the prior (absent) deadline before the next phase starts.
        self.assertGreater(timings["recoveryCleanup"]["elapsedSeconds"],
                           migration.ROLLBACK_CADDY_BUDGET_SECONDS)
        self.assertEqual(migration.ROLLBACK_SERVER_BUDGET_SECONDS,
                         timings["rollback-server.sh"]["budgetSeconds"])
        self.assertEqual(migration.ROLLBACK_CADDY_BUDGET_SECONDS,
                         timings["rollback-caddy.sh"]["budgetSeconds"])
        for name in ("rollback-server.sh", "rollback-caddy.sh"):
            self.assertEqual("pass", timings[name]["outcome"])

    def test_recovery_budgets_fit_every_enclosing_stop_and_controller_ceiling(self):
        controller = Path(__file__).with_name("offline-migration.ps1").read_text(encoding="utf-8")
        stop = int(re.search(r"--property=TimeoutStopSec=(\d+)", controller).group(1))
        terminal = int(re.search(r"function Wait-CSXMigrationTerminal \{.*?AddSeconds\((\d+)\)",
                                 controller, re.S).group(1))
        non_sql = int(re.search(
            r"Set-DeployPhase offline-migration \(\$MigrationTimeoutSeconds \+ (\d+)\)",
            controller).group(1))
        deploy = Path(__file__).with_name("deploy.ps1").read_text(encoding="utf-8")
        phase = int(re.search(r"Set-DeployPhase host-recovery (\d+)", deploy).group(1))
        wrapper = Path(__file__).with_name("deploy-production.ps1").read_text(encoding="utf-8")
        published = int(re.search(r"hostRecovery = (\d+)", wrapper).group(1))

        # The finalizer's phases and the unit's stop allowance are one contract.
        self.assertEqual(migration.RECOVERY_STOP_ALLOWANCE_SECONDS, stop)
        self.assertEqual(migration.ROLLBACK_SERVER_BUDGET_SECONDS + migration.ROLLBACK_CADDY_BUDGET_SECONDS,
                         migration.ROLLBACK_RESERVE_SECONDS)
        self.assertEqual(migration.RECOVERY_CLEANUP_BUDGET_SECONDS + migration.ROLLBACK_RESERVE_SECONDS,
                         migration.RECOVERY_BUDGET_SECONDS)
        # ExecStopPost also pays interpreter startup, the lock proof and durable
        # evidence writes, so the sum must leave real margin under the ceiling.
        self.assertLessEqual(migration.RECOVERY_BUDGET_SECONDS + 60, stop)
        # An exhausted cleanup must still leave a meaningful exact-restoration
        # reserve, never a token one.
        self.assertGreaterEqual(migration.ROLLBACK_RESERVE_SECONDS,
                                migration.RECOVERY_BUDGET_SECONDS // 3)
        # The controller must outlast the host it observes, and its phase must
        # still cover that wait plus both bounded 15-second evidence reads.
        self.assertGreaterEqual(terminal, stop)
        self.assertGreaterEqual(phase, terminal + 30)
        self.assertEqual(phase, published)
        # The supervisor's non-SQL allowance wraps the same stop window, so it
        # must not expire first and abandon a finalizer that is still bounded.
        self.assertGreaterEqual(non_sql, stop)
        self.assertIn("offlineMigration = $MigrationTimeoutSeconds + " + str(non_sql), wrapper)

    def slow_poll_query(self, now, state):
        """Answer server-backend observations so exactly one outlives the grace."""
        base = self.host.query

        def query(sql):
            if "pg_terminate_backend" in sql and "pid=2718" in sql:
                state["terminated"] = True
                return [True]
            if "client_addr=ANY" in sql:
                if not state["terminated"]:
                    now[0] += 1
                    return [SERVER_BACKEND_ROW]
                state["observations"] += 1
                now[0] += 11
                return [SERVER_BACKEND_ROW] if state["observations"] == 1 else []
            return base(sql)

        return query

    def test_rollback_server_terminate_grace_observes_again_after_one_slow_poll(self):
        # The same re-observation contract the owned-helper grace window has:
        # an in-flight observation that outlives the window still decides the
        # outcome, so a pressured host is never failed for its own latency.
        now, state = [0.0], {"terminated": False, "observations": 0}
        patch.object(migration.time, "monotonic", lambda: now[0]).start()
        patch.object(migration.time, "sleep", lambda _: None).start()
        self.host.server_present = True
        self.host.server_backend_present = True
        self.host.query = self.slow_poll_query(now, state)

        self.host.stop_server_for_rollback()

        self.assertEqual(2, state["observations"])
        self.assertEqual("pass", self.host.evidence["rollbackServerCleanup"])

    def test_rollback_server_terminate_grace_still_refuses_a_surviving_backend(self):
        now = [0.0]
        patch.object(migration.time, "monotonic", lambda: now[0]).start()
        patch.object(migration.time, "sleep", lambda _: None).start()
        self.host.server_present = True
        self.host.server_backend_present = True
        base = self.host.query

        def query(sql):
            if "pg_terminate_backend" in sql and "pid=2718" in sql:
                return [True]
            if "client_addr=ANY" in sql:
                now[0] += 11
                return [SERVER_BACKEND_ROW]
            return base(sql)

        self.host.query = query
        with self.assertRaisesRegex(RuntimeError, "server PostgreSQL backend survived termination"):
            self.host.stop_server_for_rollback()
        self.assertNotIn("rollbackServerCleanup", self.host.evidence)

    def test_quiescence_terminate_grace_observes_again_after_one_slow_poll(self):
        # stop_builders runs the same grace shape, and it is the loop behind
        # the 36.885s and 44.794s production quiescence failures.
        now, state = [0.0], {"terminated": False, "observations": 0}
        patch.object(migration.time, "monotonic", lambda: now[0]).start()
        patch.object(migration.time, "sleep", lambda _: None).start()
        self.host.backend_present = False
        self.host.server_backend_present = True
        self.host.inspect = lambda _: {"State": {"StartedAt": "2026-09-09T01:00:00Z"},
                                       "NetworkSettings": {"Networks": {
                                           "default": {"IPAddress": "172.20.0.4"}}}}
        self.host.query = self.slow_poll_query(now, state)

        self.host.stop_builders()

        self.assertEqual(2, state["observations"])
        self.assertEqual("pass", self.host.evidence["quiescence"])
        self.assertEqual("pass", self.host.evidence["serverBackendCleanup"])

    def test_quiescence_terminate_grace_still_refuses_a_surviving_backend(self):
        now = [0.0]
        patch.object(migration.time, "monotonic", lambda: now[0]).start()
        patch.object(migration.time, "sleep", lambda _: None).start()
        self.host.backend_present = False
        self.host.server_backend_present = True
        self.host.inspect = lambda _: {"State": {"StartedAt": "2026-09-09T01:00:00Z"},
                                       "NetworkSettings": {"Networks": {
                                           "default": {"IPAddress": "172.20.0.4"}}}}
        base = self.host.query

        def query(sql):
            if "pg_terminate_backend" in sql and "pid=2718" in sql:
                return [True]
            if "client_addr=ANY" in sql:
                now[0] += 11
                return [SERVER_BACKEND_ROW]
            return base(sql)

        self.host.query = query
        with self.assertRaisesRegex(RuntimeError, "server PostgreSQL backend survived termination"):
            self.host.stop_builders()
        self.assertNotEqual("pass", self.host.evidence.get("quiescence"))

    def test_late_server_backend_grace_observes_again_after_one_slow_poll(self):
        # helperCleanup's late original-server window has the same contract.
        now, state = [0.0], {"terminated": False, "observations": 0}
        patch.object(migration.time, "monotonic", lambda: now[0]).start()
        patch.object(migration.time, "sleep", lambda _: None).start()
        self.host.helper_present = False
        self.host.backend_present = False
        self.host.evidence.update({
            "quiescence": "pass",
            "originalServerNetwork": {"addresses": ["172.20.0.4"],
                                      "startedAt": "2026-09-09T01:00:00Z"},
        })
        self.host.query = self.slow_poll_query(now, state)

        self.host.cleanup_helper()

        self.assertEqual(2, state["observations"])
        self.assertEqual("pass", self.host.evidence["cleanup"])


class ProcessDeadlineTests(unittest.TestCase):
    @unittest.skipUnless(os.name == "posix", "production process groups require POSIX")
    def test_timeout_kills_shell_descendant_before_returning(self):
        # Real process tree, not a mocked subprocess: the child ignores parent
        # death and would keep running if only the direct shell were killed.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            child_pid = root / "child.pid"
            program = root / "tree.py"
            program.write_text(
                "import subprocess,sys,time\n"
                "from pathlib import Path\n"
                "p=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)'])\n"
                "Path(sys.argv[1]).write_text(str(p.pid))\n"
                "time.sleep(60)\n")
            host = migration.Host.__new__(migration.Host)
            host.root = root
            host.operation_deadline = None
            with self.assertRaises(subprocess.TimeoutExpired):
                host.command([sys.executable, str(program), str(child_pid)], seconds=1)
            pid = int(child_pid.read_text())
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline:
                status = Path("/proc") / str(pid) / "stat"
                if not status.exists() or status.read_text().split()[2] == "Z":
                    break
                time.sleep(0.05)
            else:
                self.fail("timed-out command left a running descendant")


class SourceIdentityTests(unittest.TestCase):
    def setUp(self):
        self.shell = os.environ.get("CSX_TEST_PWSH") or shutil.which("pwsh") or shutil.which("powershell")
        if not self.shell:
            self.skipTest("PowerShell unavailable")
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.control = self.base / "control"
        self.payload = self.base / "payload"
        for repo in (self.control, self.payload):
            repo.mkdir()
            self.git(repo, "init", "-q")
            self.git(repo, "config", "user.email", "test@example.invalid")
            self.git(repo, "config", "user.name", "Test")
        helper = self.control / "deploy" / "lightsail" / "deployment-source.ps1"
        helper.parent.mkdir(parents=True)
        shutil.copyfile(Path(__file__).with_name("deployment-source.ps1"), helper)
        (self.payload / "payload.txt").write_text("exact payload")
        for repo in (self.control, self.payload):
            self.git(repo, "add", ".")
            self.git(repo, "commit", "-qm", "fixture")
        self.control_sha = self.git(self.control, "rev-parse", "HEAD").strip()
        self.payload_sha = self.git(self.payload, "rev-parse", "HEAD").strip()
        self.script = self.base / "check.ps1"
        self.script.write_text("""param($Helper,$Payload,$Target,$Control)
$ErrorActionPreference='Stop'
. $Helper
Resolve-CSXDeploymentSource $Payload $Target $Control | ConvertTo-Json -Compress
""")

    def git(self, repo, *args):
        return subprocess.check_output(["git", "-C", str(repo), *args], text=True, stderr=subprocess.PIPE)

    def check(self, target=None, control=None, payload=None):
        return subprocess.run([self.shell, "-NoProfile", "-File", str(self.script),
            "-Helper", str(self.control / "deploy" / "lightsail" / "deployment-source.ps1"),
            "-Payload", str(payload or self.payload), "-Target", target or self.payload_sha,
            "-Control", control or self.control_sha], capture_output=True, text=True)

    def test_distinct_clean_control_and_payload_are_accepted(self):
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stderr)
        value = json.loads(result.stdout)
        self.assertEqual(value["Revision"], self.payload_sha)
        self.assertEqual(value["OperationalRevision"], self.control_sha)

    def test_wrong_payload_or_control_sha_is_rejected(self):
        self.assertNotEqual(self.check(target="0" * 40).returncode, 0)
        self.assertNotEqual(self.check(control="0" * 40).returncode, 0)

    @unittest.skipUnless(os.name == "nt", "Windows short-path alias regression")
    def test_windows_short_path_alias_is_the_same_repository_root(self):
        import ctypes
        short_path = ctypes.windll.kernel32.GetShortPathNameW
        short_path.argtypes = [ctypes.c_wchar_p, ctypes.c_wchar_p, ctypes.c_ulong]
        short_path.restype = ctypes.c_ulong
        buffer = ctypes.create_unicode_buffer(32768)
        length = short_path(str(self.payload), buffer, len(buffer))
        self.assertGreater(length, 0)
        self.assertLess(length, len(buffer))
        alias = buffer.value
        self.assertNotEqual(alias.lower(), str(self.payload).lower(), "fixture needs an actual 8.3 alias")
        result = self.check(payload=alias)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["Revision"], self.payload_sha)

    def test_nested_directory_and_git_directory_are_not_repository_roots(self):
        nested = self.payload / "nested"
        nested.mkdir()
        for source in (nested, self.payload / ".git"):
            result = self.check(payload=source)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("payload source must be a repository root", result.stderr)

    def test_dirty_payload_and_untracked_control_are_rejected(self):
        (self.payload / "payload.txt").write_text("dirty")
        self.assertNotEqual(self.check().returncode, 0)
        self.git(self.payload, "checkout", "--", "payload.txt")
        (self.control / "unreviewed.ps1").write_text("unreviewed")
        self.assertNotEqual(self.check().returncode, 0)


if __name__ == "__main__":
    unittest.main()
