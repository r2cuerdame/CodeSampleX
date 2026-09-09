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
        self.server_present = False
        self.server_backend_present = False
        self.server_image = IMAGE
        self.helper_environment = {"CSX_DSN": "postgres://db/csx?application_name=" + self.application,
                                   "PGAPPNAME": self.application}
        self.after = {"samples": 1, "receipts": 2, "pass": 3, "fail": 4}
        self.evidence["sourceBefore"] = self.after.copy()

    def command(self, args, seconds=30, check=True, environment=None):
        self.calls.append(("command", args))
        if args[0] == "sh" and Path(args[1]).name == self.rollback_failure:
            raise RuntimeError("injected rollback failure")
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
            "State": {"Running": self.helper_running, "ExitCode": self.helper_exit, "OOMKilled": False}}

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
            return {"version": "0036_builder_projections.sql", "count": 37}
        if "pg_get_indexdef" in sql:
            return [{"name": name, "valid": True, "ready": True, "definition": value}
                    for name, value in migration.INDEXES.items()]
        if "'repairRequired'" in sql:
            return {"samples": self.stale, "receipts": 0, "repairRequired": True}
        raise AssertionError("unexpected SQL")

    def source_totals(self):
        return self.after.copy()

    def verify_image(self, name, image, revision):
        self.calls.append(("identity", (name, image, revision)))


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
            "expectedMigration": "0036_builder_projections.sql", "migrationTimeoutSeconds": 60}))
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

    def test_no_controller_ack_times_out_without_committing_candidate(self):
        with self.assertRaisesRegex(RuntimeError, "acknowledgement deadline"):
            self.host.await_commit()
        self.assertNotEqual(self.host.evidence["phase"], "committed")
        self.host.finalize()
        self.assertEqual(self.host.evidence["phase"], "rolled-back")

    def test_wrong_payload_ack_is_rejected(self):
        (self.state / "commit.json").write_text(json.dumps({"targetSha": PREVIOUS}))
        with self.assertRaisesRegex(RuntimeError, "identity mismatch"):
            self.host.await_commit()

    def test_matching_ack_commits_only_after_lock_and_image_proof(self):
        ack = {k: self.host.evidence[k] for k in ("owner", "operationalSha", "targetSha", "imageDigest")}
        (self.state / "commit.json").write_text(json.dumps(ack))
        self.host.await_commit()
        self.assertEqual(self.host.evidence["controllerSmoke"], "acknowledged")
        self.assertEqual(self.host.evidence["phase"], "committed")
        self.assertTrue(any(k == "identity" for k, _ in self.host.calls))

    def test_changed_lock_rejects_otherwise_correct_ack(self):
        ack = {k: self.host.evidence[k] for k in ("owner", "operationalSha", "targetSha", "imageDigest")}
        (self.state / "commit.json").write_text(json.dumps(ack))
        (self.root.parent / ".deploy-lock" / "owner").write_text("0" * 32)
        with self.assertRaisesRegex(RuntimeError, "ownership changed"):
            self.host.await_commit()

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

    def test_only_migration_ledger_is_an_acceptance_query(self):
        self.host.verify_migration()
        self.assertEqual(self.host.evidence["migrationVerification"], "pass")
        queries = [q for k, q in self.host.calls if k == "sql"]
        self.assertEqual(len(queries), 1)
        self.assertIn("schema_migrations", queries[0])
        self.host.query = lambda _: {"version": "0035_previous.sql", "count": 36}
        with self.assertRaisesRegex(RuntimeError, "ledger does not match"):
            self.host.verify_migration()

    def test_recovery_script_restores_dist_before_old_container_recreation(self):
        script = Path(__file__).with_name("rollback-server.sh").read_text()
        self.assertLess(script.index("mv /opt/codesamplex/dist.rollback-stage /opt/codesamplex/dist"),
                        script.index("docker compose up -d --no-build --no-deps --force-recreate server"))

    def test_supervisor_uses_type_exec_and_a_separate_stop_budget(self):
        script = Path(__file__).with_name("offline-migration.ps1").read_text()
        self.assertIn("--property=Type=exec", script)
        self.assertIn("--property=RuntimeMaxSec=", script)
        self.assertIn("--property=TimeoutStopSec=480", script)
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

    def check(self, target=None, control=None):
        return subprocess.run([self.shell, "-NoProfile", "-File", str(self.script),
            "-Helper", str(self.control / "deploy" / "lightsail" / "deployment-source.ps1"),
            "-Payload", str(self.payload), "-Target", target or self.payload_sha,
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

    def test_dirty_payload_and_untracked_control_are_rejected(self):
        (self.payload / "payload.txt").write_text("dirty")
        self.assertNotEqual(self.check().returncode, 0)
        self.git(self.payload, "checkout", "--", "payload.txt")
        (self.control / "unreviewed.ps1").write_text("unreviewed")
        self.assertNotEqual(self.check().returncode, 0)


if __name__ == "__main__":
    unittest.main()
