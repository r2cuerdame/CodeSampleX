"""Deterministic retained-owner reconciliation tests; no Docker, DB, or host."""
import base64
import importlib.util
import io
import json
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from types import SimpleNamespace
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("reconcile", Path(__file__).with_name("reconcile-committed.py"))
reconcile = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reconcile)
OWNER = "a" * 32
TARGET = "b" * 40
PREVIOUS = "c" * 40
CONTROL = "d" * 40
IMAGE = "sha256:" + "e" * 64
STARTED = "2026-09-11T14:15:58.802547Z"
SECRET = "postgres://secret:must-not-appear@example.invalid/csx"


class FakeHost(reconcile.ReadOnlyHost):
    def __init__(self, expected, root):
        super().__init__(expected, root, root.parent / "proc")
        self.calls = []
        self.systemd = {"Id": self.unit, "LoadState": "not-found", "ActiveState": "inactive",
                        "SubState": "dead", "MainPID": "0", "ControlPID": "0", "ControlGroup": ""}
        self.systemd_exit = 1
        self.server = {"Image": IMAGE, "Config": {"Env": ["CSX_VERSION=" + TARGET, "CSX_DSN=" + SECRET]},
                       "RestartCount": 0, "State": {"Running": True, "OOMKilled": False, "Paused": False,
                                                  "Restarting": False, "StartedAt": STARTED}}
        self.image_revision = TARGET
        self.release = "v0.1.158"
        self.health = "ok"
        self.served_revision = TARGET
        self.proxy_revision = TARGET
        self.proxy_status = "200"
        self.features = '<link rel="canonical" href="https://codesamplex.dev/features">'
        self.current_ledger = self.ledger()
        self.current_indexes = indexes()
        self.invalid = 0
        self.backends = 0
        self.progress = 0
        self.helper_present = False
        self.on_call = lambda _: None

    def command(self, args, seconds=30, check=True, environment=None):
        self.calls.append(args)
        self.on_call(args)
        output, code = "", 0
        if args[0] == "systemctl":
            self.assert_command(args, ["systemctl", "show", self.unit, "--no-pager",
                "--property=Id,LoadState,ActiveState,SubState,MainPID,ControlPID,ControlGroup"])
            output = "\n".join(key + "=" + value for key, value in self.systemd.items())
            code = self.systemd_exit
        elif args[0] == "curl":
            if args[-1].endswith("/healthz"):
                output = self.health
            elif args[-1].endswith("/features"):
                output = self.features
            elif args[-1].endswith("/version"):
                output = json.dumps({"revision": self.proxy_revision})
            else:
                raise AssertionError("unexpected HTTP route")
            output += "\n" + self.proxy_status
        elif args[:2] == ["docker", "inspect"]:
            self.assert_command(args, ["docker", "inspect", "codesamplex-server-1"])
            output = json.dumps([self.server])
        elif args[:3] == ["docker", "image", "inspect"]:
            self.assert_command(args, ["docker", "image", "inspect", IMAGE])
            output = json.dumps([{"Config": {"Labels": {"org.opencontainers.image.revision": self.image_revision}}}])
        elif args[:3] == ["docker", "ps", "-aq"]:
            output = "owned-helper" if self.helper_present else ""
        elif args[:3] == ["docker", "exec", "codesamplex-server-1"]:
            self.assert_command(args, ["docker", "exec", "codesamplex-server-1", "cat", "/data/dist/.release-tag"])
            output = self.release
        elif args[:4] == ["docker", "compose", "exec", "-T"]:
            if "psql" in args:
                sql = args[-1]
                if "max(version)" in sql:
                    value = self.current_ledger
                elif "pg_get_indexdef" in sql:
                    value = self.current_indexes
                elif "pg_stat_progress_create_index" in sql:
                    value = self.progress
                elif "pg_stat_activity" in sql:
                    value = self.backends
                elif "pg_index" in sql:
                    value = self.invalid
                else:
                    raise AssertionError("unexpected SQL")
                output = json.dumps(value)
            elif args[-1] == "http://127.0.0.1:8080/healthz":
                output = self.health
            elif args[-1] == "http://127.0.0.1:8080/version":
                output = json.dumps({"revision": self.served_revision})
            else:
                raise AssertionError("unexpected compose exec")
        else:
            raise AssertionError("unreviewed command attempted")
        if code and check:
            raise RuntimeError(SECRET)
        return subprocess.CompletedProcess(args, code, output, SECRET)

    @staticmethod
    def assert_command(actual, expected):
        if actual != expected:
            raise AssertionError("unreviewed command attempted")


def indexes():
    return [{"name": name, "valid": True, "ready": True, "definition": definition}
            for name, definition in reconcile.migration.REVIEWED_MIGRATIONS["0037_slow_query_indexes.sql"]["indexes"].items()]


class ReconciliationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / "deploy"
        self.state = self.root / (".migration-" + OWNER)
        self.state.mkdir(parents=True)
        self.lock = self.root.parent / ".deploy-lock"
        self.lock.mkdir()
        (self.lock / "owner").write_text(OWNER + "\n")
        (self.root.parent / "proc").mkdir()
        self.expected = {
            "schemaVersion": 1, "owner": OWNER, "operationalSha": CONTROL,
            "targetSha": TARGET, "previousSha": PREVIOUS, "imageDigest": IMAGE,
            "previousImageDigest": "sha256:" + "f" * 64, "expectedReleaseTag": "v0.1.158",
            "expectedMigration": "0037_slow_query_indexes.sql", "expectedMigrationCount": 38,
            "migrationTimeoutSeconds": 1200, "serverStartedAt": STARTED,
        }
        self.config = {key: self.expected[key] for key in reconcile.CONFIG_FIELDS}
        self.evidence = {
            "schemaVersion": 1, "owner": OWNER, "unit": "csx-migration-" + OWNER + ".service",
            "operationalSha": CONTROL, "targetSha": TARGET, "imageDigest": IMAGE,
            "migrationTimeoutSeconds": 1200, "phase": "committed", "conclusion": "success",
            "acceptanceAuthority": "host", "controllerSmoke": "host-verified", "health": "ok",
            "proxyHealth": "ok", "smoke": "pass", "representativeSmoke": "pass", "cleanup": "pass",
            "migrationVerification": "pass", "servedRevision": TARGET, "releaseTag": "v0.1.158",
            "serverStartedAt": STARTED, "migrationLedger": {"version": "0037_slow_query_indexes.sql", "count": 38},
            "indexes": indexes(), "rollback": "not-started",
            "backends": [{"pid": 1234, "backendStart": "2026-09-11 14:15:00+00",
                          "applicationName": "csx-migrate-" + OWNER, "userName": "csx"}],
        }
        self.write_evidence()
        self.host = FakeHost(self.expected, self.root)

    def write_evidence(self):
        (self.state / "config.json").write_text(json.dumps(self.config))
        raw = (json.dumps(self.evidence, separators=(",", ":")) + "\n").encode()
        (self.state / "evidence.json").write_bytes(raw)
        self.expected["evidenceSha256"] = hashlib.sha256(raw).hexdigest()

    def collect(self):
        return reconcile.collect(self.expected, self.root, lambda *args: self.host)

    def refused(self, stage=None, code=None):
        result = self.collect()
        self.assertEqual("failure", result["conclusion"], result)
        self.assertEqual("unverified", result["lockDisposition"])
        if stage:
            self.assertEqual(stage, result["failureStage"], result)
        if code:
            self.assertEqual(code, result["failureCode"], result)
        self.assertNotIn(SECRET, json.dumps(result))
        self.assertTrue((self.lock / "owner").exists())
        return result

    def test_success_preserves_lock_and_all_retained_bytes_with_read_only_commands(self):
        paths = [self.lock / "owner", self.state / "config.json", self.state / "evidence.json"]
        before = {path: path.read_bytes() for path in paths}
        result = self.collect()
        self.assertEqual("success", result["conclusion"], result)
        self.assertEqual("retained", result["lockDisposition"])
        self.assertEqual(STARTED, result["serverStartedAt"])
        self.assertEqual(self.expected["evidenceSha256"], result["retainedEvidenceSha256"])
        self.assertEqual(before, {path: path.read_bytes() for path in paths})
        self.assertEqual({"config.json", "evidence.json"}, {path.name for path in self.state.iterdir()})
        self.assertEqual(9, len(result["checks"]))
        self.assertNotIn(SECRET, json.dumps(result))
        queries = [args for args in self.host.calls if "psql" in args]
        self.assertTrue(queries)
        self.assertTrue(all(args[-1].lstrip().startswith("SELECT ") for args in queries))
        self.assertTrue(all(any("default_transaction_read_only=on" in arg for arg in args) for args in queries))
        self.assertTrue(any("pid=1234" in args[-1] and "backend_start='2026-09-11 14:15:00+00'" in args[-1]
                            for args in queries))
        probes = [args for args in self.host.calls if args[0] == "curl"]
        self.assertEqual(["/healthz", "/features", "/version"],
                         [args[-1][len("https://codesamplex.dev"):] for args in probes])
        self.assertTrue(all("--resolve" in args and "-k" not in args and "--retry" not in args for args in probes))
        with self.assertRaises(reconcile.Refusal):
            self.host.save(phase="committed")
        with self.assertRaises(reconcile.Refusal):
            self.host.query("UPDATE stats_daily SET stats='{}'")

    def test_expected_identity_must_be_typed_exact_and_not_injectable(self):
        for key in reconcile.IDENTITY_FIELDS:
            with self.subTest(field=key):
                original = self.expected.pop(key)
                self.refused("expected")
                self.expected[key] = original
        for key, value in (("owner", "../../owner"), ("targetSha", TARGET.upper()),
                           ("serverStartedAt", 20260911), ("serverStartedAt", STARTED[:-1]),
                           ("serverStartedAt", "2026-09-11T14:15:58.802547+09:00"),
                           ("expectedMigrationCount", True), ("migrationTimeoutSeconds", True),
                           ("schemaVersion", True), ("previousSha", TARGET)):
            with self.subTest(field=key, value=value):
                original = self.expected[key]
                self.expected[key] = value
                self.refused("expected")
                self.expected[key] = original

    def test_exact_config_fields_bind_original_previous_image_and_operational_sha(self):
        for key in reconcile.CONFIG_FIELDS:
            with self.subTest(field=key):
                original = self.config[key]
                self.config[key] = "wrong"
                self.write_evidence()
                self.refused("retained", "retained-config-mismatch")
                self.config[key] = original
        self.write_evidence()

    def test_missing_foreign_or_extra_owner_state_is_not_adopted(self):
        (self.lock / "owner").write_text("0" * 32)
        self.refused("retained", "owner-mismatch")
        (self.lock / "owner").write_text(OWNER)
        (self.lock / "unknown").write_text("present")
        self.refused("retained", "unexpected-lock-contents")
        (self.lock / "unknown").unlink()
        (self.lock / "owner").unlink()
        self.assertEqual("failure", self.collect()["conclusion"])
        self.assertFalse((self.lock / "owner").exists())

    def test_original_raw_evidence_hash_is_required_even_when_semantics_match(self):
        raw = (self.state / "evidence.json").read_bytes()
        (self.state / "evidence.json").write_bytes(raw + b"\n")
        self.refused("retained", "retained-evidence-hash-mismatch")

    def test_duplicate_json_keys_are_not_silently_accepted(self):
        path = self.state / "config.json"
        path.write_text(path.read_text().replace('"targetSha":', '"targetSha":"bad","targetSha":'))
        self.refused("retained", "duplicate-json-property")

    def test_every_committed_acceptance_proof_is_required(self):
        for key in ("phase", "conclusion", "acceptanceAuthority", "controllerSmoke", "health", "proxyHealth",
                    "smoke", "representativeSmoke", "cleanup", "migrationVerification", "servedRevision",
                    "releaseTag", "serverStartedAt", "owner", "unit", "imageDigest", "operationalSha",
                    "targetSha", "migrationTimeoutSeconds", "schemaVersion"):
            with self.subTest(field=key):
                original = self.evidence.pop(key)
                self.write_evidence()
                self.refused("retained", "retained-acceptance-mismatch")
                self.evidence[key] = original
        self.write_evidence()

    def test_rollback_or_missing_index_evidence_blocks_commit_reconciliation(self):
        self.evidence["rollback"] = "succeeded"
        self.write_evidence()
        self.refused("retained", "retained-rollback-evidence")
        self.evidence["rollback"] = "not-started"
        self.evidence["indexes"].pop()
        self.write_evidence()
        self.refused("retained", "required-indexes-missing")

    def test_terminal_status_includes_exec_stop_post_and_process_inventory(self):
        for key, value in (("ActiveState", "deactivating"), ("SubState", "stop-post"),
                           ("MainPID", "123"), ("ControlPID", "456"), ("LoadState", "error"),
                           ("Id", "another.service"), ("ControlGroup", "/foreign")):
            with self.subTest(field=key):
                original = self.host.systemd[key]
                self.host.systemd[key] = value
                self.refused("supervisor-before")
                self.host.systemd[key] = original
        self.host.systemd = {}
        self.refused("supervisor-before", "missing-supervisor-state")

    def test_unloaded_supervisor_cannot_hide_running_finalizer_or_cgroup_descendant(self):
        process = self.root.parent / "proc" / "999"
        process.mkdir()
        (process / "cmdline").write_bytes(b"python3\0" + str(self.state / "offline-migration.py").encode() +
                                         b"\0finalize\0" + OWNER.encode())
        (process / "cgroup").write_text("0::/other")
        self.refused("supervisor-before", "supervisor-process-active")
        (process / "cmdline").write_bytes(b"docker\0compose")
        (process / "cgroup").write_text("0::/system.slice/" + self.host.unit + "/child")
        self.refused("supervisor-before", "supervisor-cgroup-active")

    def test_fresh_database_ledger_indexes_cleanup_must_pass(self):
        for attribute, value in (("current_ledger", {"version": "0036_builder_projections.sql", "count": 37}),
                                  ("invalid", 1), ("progress", 1), ("backends", 1), ("helper_present", True)):
            with self.subTest(attribute=attribute):
                original = getattr(self.host, attribute)
                setattr(self.host, attribute, value)
                self.refused("database")
                setattr(self.host, attribute, original)
        for key, value in (("valid", False), ("ready", False), ("definition", "CREATE INDEX wrong")):
            original = self.host.current_indexes[0][key]
            self.host.current_indexes[0][key] = value
            self.refused("database")
            self.host.current_indexes[0][key] = original

    def test_live_identity_and_health_never_fall_back_to_retained_success(self):
        for attribute, value, stage in (("image_revision", PREVIOUS, "identity-before"),
                                       ("release", "v9.9.9", "identity-before"),
                                       ("health", "database unavailable", "health-smoke"),
                                       ("served_revision", PREVIOUS, "health-smoke"),
                                       ("proxy_revision", PREVIOUS, "health-smoke"),
                                       ("proxy_status", "503", "health-smoke"),
                                       ("features", "", "health-smoke")):
            with self.subTest(attribute=attribute):
                original = getattr(self.host, attribute)
                setattr(self.host, attribute, value)
                self.refused(stage)
                setattr(self.host, attribute, original)
        for key, value in (("Running", False), ("OOMKilled", True), ("Paused", True),
                           ("Restarting", True), ("StartedAt", STARTED.replace("58.", "59."))):
            original = self.host.server["State"][key]
            self.host.server["State"][key] = value
            self.refused("identity-before")
            self.host.server["State"][key] = original
        self.host.server["Config"]["Env"].append("CSX_VERSION=" + TARGET)
        self.refused("identity-before", "configured-revision-mismatch")

    def test_container_restarted_after_smoke_is_refused(self):
        def changed(args):
            if args[0] == "curl" and args[-1].endswith("/version"):
                self.host.server["State"]["StartedAt"] = "2026-09-11T14:16:00Z"
        self.host.on_call = changed
        self.refused("identity-after", "server-start-changed")

    def test_finalizer_becomes_active_after_smoke_is_refused(self):
        def changed(args):
            if args[0] == "curl" and args[-1].endswith("/version"):
                self.host.systemd["ControlPID"] = "123"
        self.host.on_call = changed
        self.refused("supervisor-after", "supervisor-not-terminal")

    def test_lock_config_and_evidence_changes_during_checks_are_refused(self):
        for filename in ("owner", "config.json", "evidence.json"):
            with self.subTest(filename=filename):
                self.setUp()
                path = self.lock / filename if filename == "owner" else self.state / filename
                def changed(args):
                    if args[0] == "curl" and args[-1].endswith("/version"):
                        path.write_bytes(path.read_bytes() + b"\n")
                self.host.on_call = changed
                self.refused("retained-stable", "retained-state-changed")

    def test_replacing_owner_file_with_identical_bytes_is_refused(self):
        def changed(args):
            if args[0] == "curl" and args[-1].endswith("/version"):
                path = self.lock / "owner"
                replacement = self.lock / "replacement"
                replacement.write_bytes(path.read_bytes())
                os.replace(replacement, path)
        self.host.on_call = changed
        self.refused("retained-stable", "retained-state-changed")

    @unittest.skipUnless(os.name == "posix", "POSIX permissions and symlink semantics")
    def test_symlink_and_writable_retained_state_are_refused(self):
        path = self.state / "evidence.json"
        path.chmod(0o666)
        self.refused("retained", "writable-retained-state")
        path.chmod(0o600)
        raw = path.read_bytes()
        path.unlink()
        sibling = self.state / "other.json"
        sibling.write_bytes(raw)
        path.symlink_to(sibling)
        self.refused("retained", "unsafe-retained-path")

    @unittest.skipUnless(os.name == "posix", "POSIX private-group directory permissions")
    def test_private_775_deploy_directory_does_not_relax_other_retained_paths(self):
        uid, gid = os.geteuid(), os.getegid()
        account = SimpleNamespace(pw_uid=uid, pw_gid=gid, pw_name="fixture")
        group = SimpleNamespace(gr_gid=gid, gr_name="fixture", gr_mem=[])
        self.root.chmod(0o775)
        with patch.dict(sys.modules, {
            "pwd": SimpleNamespace(getpwuid=lambda _: account, getpwall=lambda: [account]),
            "grp": SimpleNamespace(getgrgid=lambda _: group),
        }):
            result = self.collect()
            self.assertEqual("success", result["conclusion"], result)
            for path in (self.root.parent, self.lock, self.state,
                         self.lock / "owner", self.state / "config.json", self.state / "evidence.json"):
                with self.subTest(path=path.name):
                    original = path.stat().st_mode
                    path.chmod(original | 0o020)
                    self.refused("retained", "writable-retained-state")
                    path.chmod(original)

    def test_timeout_and_command_errors_cannot_leak_raw_output(self):
        def timeout(args):
            raise subprocess.TimeoutExpired(["command", SECRET], 10, output=SECRET, stderr=SECRET)
        self.host.on_call = timeout
        self.refused("supervisor-before", "evidence-invalid-or-unavailable")

    def test_whole_collection_has_one_wall_clock_budget(self):
        now = [0]
        def tick(args):
            now[0] += 20
        self.host.on_call = tick
        self.host.operation_deadline = 180
        with patch.object(reconcile.time, "monotonic", lambda: now[0]):
            self.refused(code="reconciliation-deadline-exceeded")

    def test_main_json_failure_and_exact_timestamp_input(self):
        encoded = base64.b64encode(json.dumps(self.expected).encode()).decode()
        with patch.object(reconcile, "collect", return_value={"conclusion": "success"}) as collect:
            with redirect_stdout(io.StringIO()) as output:
                code = reconcile.main(["reconcile-committed.py", "--expected-base64", encoded])
        self.assertEqual(0, code)
        self.assertEqual(STARTED, collect.call_args[0][0]["serverStartedAt"])
        self.assertEqual({"conclusion": "success"}, json.loads(output.getvalue()))
        with redirect_stdout(io.StringIO()) as output:
            code = reconcile.main(["reconcile-committed.py", "--expected-base64", "invalid"])
        self.assertEqual(1, code)
        self.assertEqual("failure", json.loads(output.getvalue())["conclusion"])



class PrivateDirectoryPermissionsTests(unittest.TestCase):
    def setUp(self):
        self.account = SimpleNamespace(pw_uid=1000, pw_gid=1000, pw_name="ubuntu")
        self.accounts = [SimpleNamespace(pw_uid=0, pw_gid=0, pw_name="root"), self.account]
        self.group = SimpleNamespace(gr_gid=1000, gr_name="ubuntu", gr_mem=[])
        self.info = SimpleNamespace(st_mode=reconcile.stat.S_IFDIR | 0o775, st_uid=1000, st_gid=1000)
        self.addCleanup(patch.stopall)
        patch.object(reconcile.os, "geteuid", return_value=1000, create=True).start()
        patch.object(reconcile.os, "getegid", return_value=1000, create=True).start()
        patch.dict(sys.modules, {
            "pwd": SimpleNamespace(getpwuid=lambda _: self.account, getpwall=lambda: self.accounts),
            "grp": SimpleNamespace(getgrgid=lambda _: self.group),
        }).start()

    def test_exact_775_owner_private_primary_group_is_no_additional_writer(self):
        reconcile.verify_permissions(self.info, private_group_directory=True)
        self.group.gr_mem = ["ubuntu", "root"]
        reconcile.verify_permissions(self.info, private_group_directory=True)

    def test_group_permission_exception_never_applies_to_retained_state_or_files(self):
        with self.assertRaisesRegex(reconcile.Refusal, "writable-retained-state"):
            reconcile.verify_permissions(self.info)
        self.info.st_mode = reconcile.stat.S_IFREG | 0o664
        with self.assertRaisesRegex(reconcile.Refusal, "writable-retained-state"):
            reconcile.verify_permissions(self.info, private_group_directory=True)

    def test_world_write_foreign_owner_gid_and_special_directory_modes_are_refused(self):
        for key, value in (("st_uid", 2000), ("st_gid", 2000),
                           ("st_mode", reconcile.stat.S_IFDIR | 0o777),
                           ("st_mode", reconcile.stat.S_IFDIR | 0o2775)):
            with self.subTest(field=key, value=value):
                original = getattr(self.info, key)
                setattr(self.info, key, value)
                with self.assertRaises(reconcile.Refusal):
                    reconcile.verify_permissions(self.info, private_group_directory=True)
                setattr(self.info, key, original)

    def test_supplementary_or_primary_other_group_members_are_refused(self):
        self.group.gr_mem = ["other"]
        with self.assertRaisesRegex(reconcile.Refusal, "untrusted-deploy-directory-group"):
            reconcile.verify_permissions(self.info, private_group_directory=True)
        self.group.gr_mem = []
        self.accounts.append(SimpleNamespace(pw_uid=2000, pw_gid=1000, pw_name="other"))
        with self.assertRaisesRegex(reconcile.Refusal, "untrusted-deploy-directory-group"):
            reconcile.verify_permissions(self.info, private_group_directory=True)

    def test_nonprivate_group_or_incomplete_account_enumeration_is_refused(self):
        self.group.gr_name = "shared"
        with self.assertRaisesRegex(reconcile.Refusal, "untrusted-deploy-directory-group"):
            reconcile.verify_permissions(self.info, private_group_directory=True)
        self.group.gr_name = "ubuntu"
        self.accounts = []
        with self.assertRaisesRegex(reconcile.Refusal, "untrusted-deploy-directory-group"):
            reconcile.verify_permissions(self.info, private_group_directory=True)


class CommandBoundaryTests(unittest.TestCase):
    def test_subprocess_detaches_stdin_and_timeout_kills_entire_process_group(self):
        host = reconcile.ReadOnlyHost.__new__(reconcile.ReadOnlyHost)
        host.root = Path(".")
        host.operation_deadline = 12
        process = unittest.mock.Mock(pid=4242, returncode=0)
        process.communicate.side_effect = subprocess.TimeoutExpired(["read-only"], 2, stderr=SECRET)
        with patch.object(reconcile.time, "monotonic", return_value=10), \
             patch.object(reconcile.subprocess, "Popen", return_value=process) as popen, \
             patch.object(reconcile.os, "killpg", create=True) as kill, \
             patch.object(reconcile.signal, "SIGKILL", 9, create=True):
            with self.assertRaises(subprocess.TimeoutExpired):
                host.command(["read-only"], seconds=10)
        self.assertEqual(subprocess.DEVNULL, popen.call_args[1]["stdin"])
        self.assertTrue(popen.call_args[1]["start_new_session"])
        process.communicate.assert_called_once_with(timeout=2)
        kill.assert_called_once_with(4242, 9)
        process.wait.assert_called_once_with(timeout=5)

    def test_nonzero_exit_is_fixed_code_and_deadline_does_not_launch(self):
        host = reconcile.ReadOnlyHost.__new__(reconcile.ReadOnlyHost)
        host.root = Path(".")
        host.operation_deadline = 12
        process = unittest.mock.Mock(pid=4242, returncode=1)
        process.communicate.return_value = (SECRET, SECRET)
        with patch.object(reconcile.time, "monotonic", return_value=10), \
             patch.object(reconcile.subprocess, "Popen", return_value=process):
            with self.assertRaisesRegex(reconcile.Refusal, "^read-only-command-failed$"):
                host.command(["read-only"])
            result = host.command(["read-only"], check=False)
            self.assertEqual(1, result.returncode)
        with patch.object(reconcile.time, "monotonic", return_value=12), \
             patch.object(reconcile.subprocess, "Popen") as popen:
            with self.assertRaisesRegex(reconcile.Refusal, "^reconciliation-deadline-exceeded$"):
                host.command(["read-only"])
            popen.assert_not_called()


if __name__ == "__main__":
    unittest.main()
