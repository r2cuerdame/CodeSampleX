"""Failure-state tests for the host supervisor; no Docker/DB/production access."""
import importlib.util
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
        self.repair_required = False
        self.barrier_count = 1
        self.barrier_armed = True
        self.index_fault = None
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
        if "WITH latest AS" in sql:
            self.repair_required = self.barrier_count == 1 and self.barrier_armed
            return {"count": self.barrier_count, "day": "2026-09-09" if self.barrier_count else None,
                    "armed": self.barrier_armed}
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
        for phase in ("committed", "rolled-back"):
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

    def test_recovery_script_restores_dist_before_old_container_recreation(self):
        script = Path(__file__).with_name("rollback-server.sh").read_text()
        self.assertLess(script.index("mv /opt/codesamplex/dist.rollback-stage /opt/codesamplex/dist"),
                        script.index("docker compose up -d --no-build --no-deps --force-recreate server"))

    def test_supervisor_uses_type_exec_and_a_separate_stop_budget(self):
        script = Path(__file__).with_name("offline-migration.ps1").read_text()
        self.assertIn("--property=Type=exec", script)
        self.assertIn("--property=RuntimeMaxSec=", script)
        self.assertIn("--property=TimeoutStopSec=240", script)
        self.assertIn("ExecStopPost=", script)
        self.assertNotIn("prestage-builder-indexes", script)


    def test_quiescence_is_proved_without_a_full_table_baseline(self):
        self.host.backend_present = False
        self.host.helper_present = False
        self.host.inspect = lambda _: {"State": {"StartedAt": "2026-09-09T01:00:00Z"}, "NetworkSettings": {"Networks": {}}}
        self.host.after["samples"] = 19
        self.host.stop_builders()
        self.assertFalse(any(k == "sql" for k, _ in self.host.calls))
        self.assertEqual("pass", self.host.evidence["quiescence"])

    def test_surviving_unowned_client_cannot_pass_empty_owned_cleanup(self):
        self.host.helper_present = False
        self.host.backend_present = False
        self.host.evidence["quiescence"] = "pass"
        self.host.clients = lambda owned_only=False: [] if owned_only else [{"pid": 42}]
        with self.assertRaisesRegex(RuntimeError, "unowned database clients"):
            self.host.cleanup_helper()

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


    def test_completed_retry_rearms_marker_without_corpus_scan(self):
        self.assertFalse(self.host.repair_required)
        self.host.verify_migration()
        self.assertTrue(self.host.repair_required)
        self.assertEqual({"count": 1, "day": "2026-09-09", "armed": True},
                         self.host.evidence["repairBarrierRearmed"])

    def test_missing_or_unarmed_current_stats_row_blocks_activation(self):
        for count, armed in ((0, False), (2, True), (1, False)):
            with self.subTest(count=count, armed=armed):
                self.host.barrier_count = count
                self.host.barrier_armed = armed
                with self.assertRaisesRegex(RuntimeError, "exactly one current stats row"):
                    self.host.verify_migration()

    def test_missing_wrong_or_not_ready_index_blocks_migration_acceptance(self):
        for fault in ("missing", "wrong", "invalid", "not-ready"):
            with self.subTest(fault=fault):
                self.host.index_fault = fault
                with self.assertRaisesRegex(RuntimeError, "indexes are missing|valid, ready and exact"):
                    self.host.verify_migration()
                self.assertFalse(self.host.repair_required)

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

    def test_proxy_unavailable_exhausts_only_its_15_second_readiness_budget(self):
        now = [0]
        self.host.proxy_status = "503"
        with patch.object(migration.time, "monotonic", lambda: now[0]), \
             patch.object(migration.time, "sleep", lambda seconds: now.__setitem__(0, now[0] + seconds)):
            with self.assertRaisesRegex(RuntimeError, "proxy health deadline"):
                self.host.activate()
        self.assertEqual(15, now[0])
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