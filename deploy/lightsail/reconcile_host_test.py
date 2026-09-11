import base64
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("reconcile_host", Path(__file__).with_name("reconcile-host.py"))
host = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(host)


def fixture():
    evidence = {
        "schemaVersion": 1, "owner": "a" * 32, "unit": "csx-migration-" + "a" * 32 + ".service",
        "operationalSha": "b" * 40, "targetSha": "c" * 40, "servedRevision": "c" * 40,
        "imageDigest": "sha256:" + "d" * 64, "releaseTag": "v0.1.158",
        "phase": "committed", "conclusion": "success", "acceptanceAuthority": "host",
        "controllerSmoke": "host-verified", "health": "ok", "proxyHealth": "ok", "smoke": "pass",
        "representativeSmoke": "pass", "cleanup": "pass", "migrationVerification": "pass",
        "rollback": "not-started", "migrationLedger": {"version": "0037_slow_query_indexes.sql", "count": 38},
        "migrationTimeoutSeconds": 1200,
        "indexes": [{"name": key, "valid": True, "ready": True, "definition": value}
                    for key, value in host.INDEXES37.items()],
        "serverStartedAt": "2026-09-11T14:15:58.802547123Z",
        "activationStartedAt": "2026-09-11T14:15:56.874578+00:00",
        "cleanupCompletedAt": "2026-09-11T14:15:52.871878+00:00",
        "completedAt": "2026-09-11T14:16:51.830430+00:00",
        "phaseTimings": {"activation": {"outcome": "pass"}},
    }
    return {"mode": "verify", "repository": "r2cuerdame/CodeSampleX", "sourceRunId": "100",
            "sourceRunAttempt": 1, "sourceArtifactId": 101, "sourceArtifactSha256": "e" * 64,
            "hostEvidenceSha256": "f" * 64, "hostEvidence": evidence,
            "previousSha": "1" * 40, "previousImageDigest": "sha256:" + "2" * 64,
            "reconciliationRunId": "200", "reconciliationRunAttempt": 1, "operationalSha": "3" * 40}


class FakeHost(host.Host):
    def __init__(self, request, root):
        super().__init__(request, root, control_home=root)
        self.commands = []
        self.failures = {}
        self.on_final_identity = None
        self.identities = 0

    @staticmethod
    def sync_directory(path):
        # Exercise actual directory fsync on Linux CI, whose host is POSIX.
        if os.name == "posix":
            host.Host.sync_directory(path)

    def command(self, args, seconds=10):
        self.commands.append(args)
        joined = " ".join(args)
        for key, value in self.failures.items():
            if key in joined:
                if isinstance(value, Exception):
                    raise value
                return value
        evidence = self.request["hostEvidence"]
        if args[0] == "systemctl":
            return "ActiveState=inactive\nSubState=dead\nMainPID=0\nControlPID=0\n"
        if args[:2] == ["docker", "inspect"]:
            self.identities += 1
            if self.identities == 2 and self.on_final_identity:
                self.on_final_identity()
            return json.dumps([{"Id": "container-id", "Image": evidence["imageDigest"],
                                "Config": {"Env": ["CSX_VERSION=" + evidence["targetSha"]]},
                                "State": {"Running": True, "OOMKilled": False,
                                          "StartedAt": evidence["serverStartedAt"]}, "RestartCount": 0}])
        if args[:3] == ["docker", "image", "inspect"]:
            return json.dumps([{"Id": evidence["imageDigest"], "Config": {"Labels": {
                "org.opencontainers.image.revision": evidence["targetSha"]}}}])
        if args[:2] == ["docker", "ps"]:
            return ""
        if "psql" in args:
            sql = args[-1]
            if "pg_stat_activity" in sql:
                return '{"owned":0,"ddl":0}'
            if "schema_migrations" in sql:
                return json.dumps(evidence["migrationLedger"])
            if "pg_index" in sql:
                return json.dumps(evidence["indexes"])
        if args[-1].endswith("/healthz"):
            return "ok\n200" if args[0] == "curl" else "ok"
        if args[-1].endswith("/version"):
            return json.dumps({"revision": evidence["targetSha"]}) + ("\n200" if args[0] == "curl" else "")
        if args[-1].endswith(".release-tag"):
            return evidence["releaseTag"]
        if args[-1].endswith("/features"):
            return '<link rel="canonical" href="https://codesamplex.dev/features">\n200'
        if args[-1].endswith("/v1/stats"):
            return '{"generatedAt":"2026-09-09T00:00:00Z"}\n200'
        if args[-1].endswith("/samples"):
            return "samples\n200"
        raise AssertionError("unexpected command: " + joined)


class ReconciliationHostTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.request = fixture()
        self.migration = self.root / "deploy" / (".migration-" + "a" * 32)
        self.migration.mkdir(parents=True)
        self.raw = (json.dumps(self.request["hostEvidence"], separators=(",", ":")) + "\n").encode()
        self.request["hostEvidenceSha256"] = hashlib.sha256(self.raw).hexdigest()
        (self.migration / "evidence.json").write_bytes(self.raw)
        evidence = self.request["hostEvidence"]
        self.config = {key: evidence[key] for key in ("targetSha", "operationalSha", "imageDigest")}
        self.config.update(previousSha=self.request["previousSha"], previousImageDigest=self.request["previousImageDigest"],
                           expectedReleaseTag=evidence["releaseTag"], expectedMigration=evidence["migrationLedger"]["version"],
                           expectedMigrationCount=38, migrationTimeoutSeconds=1200)
        (self.migration / "config.json").write_text(json.dumps(self.config))
        self.lock = self.root / ".deploy-lock"
        self.lock.mkdir()
        (self.lock / "owner").write_bytes(("a" * 32 + "\n").encode())
        self.archive = self.root / (".deploy-reconciled-" + "a" * 32)
        self.prepared = {"id": 201, "digest": "sha256:" + "4" * 64, "runId": "200", "runAttempt": 1}
        # Windows runs the same behavioral tests; the executable host always
        # has O_NOFOLLOW. Unsafe links are separately exercised on POSIX CI.
        if not hasattr(os, "O_NOFOLLOW"):
            self.no_follow = patch.object(os, "O_NOFOLLOW", 0, create=True)
            self.no_follow.start()
            self.addCleanup(self.no_follow.stop)

    def runner(self):
        return FakeHost(self.request, self.root)

    def release(self):
        self.request.update(mode="release", preparedArtifact=self.prepared)
        return self.runner().run()

    def test_verification_is_read_only_and_preserves_nanoseconds(self):
        runner = self.runner()
        result = runner.run()
        self.assertEqual(result["binding"]["serverStartedAt"], "2026-09-11T14:15:58.802547123Z")
        self.assertEqual(result["lockState"], "owned")
        self.assertIsNone(result["receipt"])
        self.assertEqual({p.name for p in self.lock.iterdir()}, {"owner"})
        self.assertFalse(self.archive.exists())
        self.assertEqual((self.migration / "evidence.json").read_bytes(), self.raw)
        for command in runner.commands:
            self.assertNotIn("--force-recreate", command)
            self.assertNotIn("stop", command)
            self.assertNotIn("rm", command)
            if "psql" in command:
                self.assertIn("default_transaction_read_only=on", " ".join(command))

    def test_terminal_commit_fields_fail_closed(self):
        for key in ("phase", "conclusion", "owner", "unit", "operationalSha", "targetSha", "servedRevision", "imageDigest",
                    "releaseTag", "acceptanceAuthority", "controllerSmoke", "health", "proxyHealth", "smoke",
                    "representativeSmoke", "cleanup", "migrationVerification", "rollback", "serverStartedAt"):
            with self.subTest(key=key):
                request = copy.deepcopy(self.request)
                request["hostEvidence"][key] = "wrong"
                with self.assertRaises((host.Refusal, ValueError)):
                    FakeHost(request, self.root).run()
        for key, value in (("previousSha", "c" * 40), ("sourceRunId", "0"), ("sourceArtifactId", True),
                           ("hostEvidenceSha256", "a" * 64), ("sourceRunAttempt", 0)):
            with self.subTest(key=key):
                request = copy.deepcopy(self.request)
                request[key] = value
                with self.assertRaises(host.Refusal):
                    FakeHost(request, self.root).run()

    def test_previous_identity_and_configuration_cannot_be_replayed(self):
        for key in self.config:
            with self.subTest(key=key):
                changed = dict(self.config, **{key: "wrong"})
                (self.migration / "config.json").write_text(json.dumps(changed))
                with self.assertRaises(host.Refusal):
                    self.runner().run()
        (self.migration / "config.json").write_text(json.dumps(self.config))

    def test_retained_bytes_cannot_change_even_if_json_is_equivalent(self):
        (self.migration / "evidence.json").write_bytes(self.raw + b" ")
        with self.assertRaisesRegex(host.Refusal, "retained-evidence-digest"):
            self.runner().run()

    def test_live_failures_never_release(self):
        failures = {
            "systemctl": "ActiveState=active\nSubState=running\nMainPID=17\nControlPID=0\n",
            "docker inspect": "[]", "docker image inspect": "[]", "docker ps": "helper-id",
            "pg_stat_activity": '{"owned":1,"ddl":0}', "schema_migrations": '{"version":"0036_builder_projections.sql","count":37}',
            "pg_index": "[]", "http://127.0.0.1:8080/healthz": host.Refusal("command-timeout"),
            "http://127.0.0.1:8080/version": '{"revision":"wrong"}', ".release-tag": "v0.1.155",
            "https://codesamplex.dev/healthz": "database unavailable\n503",
            "https://codesamplex.dev/version": '{"revision":"wrong"}\n200',
            "https://codesamplex.dev/features": "wrong-page\n200",
            "https://codesamplex.dev/v1/stats": '{}\n500', "https://codesamplex.dev/samples": "busy\n503",
        }
        self.request.update(mode="release", preparedArtifact=self.prepared)
        for key, value in failures.items():
            with self.subTest(key=key):
                runner = self.runner()
                runner.failures[key] = value
                with self.assertRaises(host.Refusal):
                    runner.run()
                self.assertEqual({p.name for p in self.lock.iterdir()}, {"owner"})
                self.assertFalse(self.archive.exists())

    def test_lock_and_evidence_changes_during_check_are_refused(self):
        runner = self.runner()
        runner.on_final_identity = lambda: (self.lock / "owner").write_text("foreign")
        with self.assertRaisesRegex(host.Refusal, "lock-owner|retained-state-changed"):
            runner.run()
        self.assertTrue(self.lock.exists())

    def test_evidence_config_and_supervisor_cannot_drift(self):
        for filename in ("evidence.json", "config.json"):
            with self.subTest(filename=filename):
                original = (self.migration / filename).read_bytes()
                runner = self.runner()
                runner.on_final_identity = lambda: (self.migration / filename).write_bytes(b"{}")
                with self.assertRaises(host.Refusal):
                    runner.run()
                (self.migration / filename).write_bytes(original)
        for properties in ("", "ActiveState=inactive\nSubState=dead\nMainPID=1\nControlPID=0\n",
                           "ActiveState=inactive\nSubState=dead\nMainPID=0\nControlPID=1\n"):
            with self.subTest(properties=properties):
                runner = self.runner()
                runner.failures["systemctl"] = properties
                with self.assertRaises(host.Refusal):
                    runner.run()

    def test_aborted_owner_cannot_be_reconciled(self):
        runner = self.runner()
        runner.abort.write_text("aborted")
        with self.assertRaisesRegex(host.Refusal, "owner-aborted"):
            runner.run()

    def test_live_exact_identity_and_index_drift_refuse_release(self):
        self.request.update(mode="release", preparedArtifact=self.prepared)
        template = json.loads(self.runner().command(["docker", "inspect", "codesamplex-server-1"]))
        changes = (
            ("image", lambda row: row.update(Image="sha256:" + "9" * 64)),
            ("configured-sha", lambda row: row["Config"].update(Env=["CSX_VERSION=" + "1" * 40])),
            ("duplicate-config", lambda row: row["Config"]["Env"].append("CSX_VERSION=" + "c" * 40)),
            ("stopped", lambda row: row["State"].update(Running=False)),
            ("oom", lambda row: row["State"].update(OOMKilled=True)),
            ("restart-count", lambda row: row.update(RestartCount=1)),
            ("same-sha-new-start", lambda row: row["State"].update(StartedAt="2026-09-11T14:15:58.802547124Z")),
        )
        for name, change in changes:
            with self.subTest(name=name):
                rows = copy.deepcopy(template)
                change(rows[0])
                runner = self.runner()
                runner.failures["docker inspect"] = json.dumps(rows)
                with self.assertRaises(host.Refusal):
                    runner.run()
                self.assertTrue(self.lock.exists())
                self.assertFalse(self.archive.exists())
        for key, value in (("valid", False), ("ready", False), ("definition", "CREATE INDEX wrong")):
            with self.subTest(index=key):
                rows = copy.deepcopy(self.request["hostEvidence"]["indexes"])
                rows[0][key] = value
                runner = self.runner()
                runner.failures["pg_index"] = json.dumps(rows)
                with self.assertRaises(host.Refusal):
                    runner.run()
                self.assertFalse(self.archive.exists())

    def test_owner_archive_is_atomic_and_retry_does_not_restart(self):
        result = self.release()
        self.assertFalse(self.lock.exists())
        self.assertEqual(result["lockState"], "archived")
        receipt_bytes = (self.archive / "reconciliation.json").read_bytes()
        self.request.update(mode="verify", reconciliationRunId="300")
        self.assertEqual(self.runner().run()["receipt"], result["receipt"])
        self.request["mode"] = "release"
        self.assertEqual(self.runner().run()["receipt"], result["receipt"])
        self.assertEqual(receipt_bytes, (self.archive / "reconciliation.json").read_bytes())

    def test_finalizer_or_owned_work_reappearing_during_smoke_keeps_lock(self):
        self.request.update(mode="release", preparedArtifact=self.prepared)
        for changes in (
            {"systemctl": "ActiveState=deactivating\nSubState=stop-post\nMainPID=0\nControlPID=42\n"},
            {"docker ps": "helper-id"},
            {"pg_stat_activity": '{"owned":1,"ddl":0}'},
            {"pg_stat_activity": '{"owned":0,"ddl":1}'},
        ):
            with self.subTest(changes=changes):
                runner = self.runner()
                runner.on_final_identity = lambda: runner.failures.update(changes)
                with self.assertRaises(host.Refusal):
                    runner.run()
                self.assertEqual({path.name for path in self.lock.iterdir()}, {"owner"})
                self.assertFalse(self.archive.exists())

    def test_replaced_lock_or_retained_file_with_identical_bytes_is_not_adopted(self):
        self.request.update(mode="release", preparedArtifact=self.prepared)
        for relative in (".deploy-lock", "deploy/.migration-" + "a" * 32 + "/config.json",
                         "deploy/.migration-" + "a" * 32 + "/evidence.json", ".deploy-lock/owner"):
            with self.subTest(path=relative), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                shutil.copytree(self.root, root, dirs_exist_ok=True)
                runner = FakeHost(self.request, root)
                original = root / relative
                def replace():
                    saved = root / "original-retained-path"
                    original.rename(saved)
                    if saved.is_dir():
                        shutil.copytree(saved, original)
                    else:
                        shutil.copyfile(saved, original)
                runner.on_final_identity = replace
                with self.assertRaisesRegex(host.Refusal, "retained-state-changed"):
                    runner.run()
                self.assertTrue(runner.lock.exists())
                self.assertFalse(runner.archive.exists())

    def test_receipt_sync_does_not_hide_extra_child_or_new_abort_marker(self):
        for extra in ("unexpected-child", "abort-marker"):
            with self.subTest(extra=extra), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                shutil.copytree(self.root, root, dirs_exist_ok=True)
                request = dict(self.request, mode="release", preparedArtifact=self.prepared)
                runner = FakeHost(request, root)
                original_sync = runner.sync_receipt
                def changed(directory):
                    original_sync(directory)
                    path = directory / "unexpected" if extra == "unexpected-child" else runner.abort
                    path.write_text("retained for inspection")
                with patch.object(runner, "sync_receipt", side_effect=changed):
                    with self.assertRaises(host.Refusal):
                        runner.run()
                self.assertTrue(runner.lock.exists())
                self.assertFalse(runner.archive.exists())

    def test_extra_child_during_receipt_creation_is_not_absorbed_into_directory_snapshot(self):
        self.request.update(mode="release", preparedArtifact=self.prepared)
        runner = self.runner()
        original_fsync = host.os.fsync
        calls = []
        def changed(descriptor):
            original_fsync(descriptor)
            if not calls:
                (self.lock / "unexpected").write_text("retained for inspection")
            calls.append(descriptor)
        with patch.object(host.os, "fsync", side_effect=changed):
            with self.assertRaisesRegex(host.Refusal, "lock-contents"):
                runner.run()
        self.assertTrue(self.lock.exists())
        self.assertFalse(self.archive.exists())

    def test_archived_retry_detects_new_owner_created_during_receipt_sync(self):
        self.release()
        runner = self.runner()
        original_sync = runner.sync_receipt
        def changed(directory):
            original_sync(directory)
            self.lock.mkdir()
            (self.lock / "owner").write_text("new-owner\n")
        with patch.object(runner, "sync_receipt", side_effect=changed):
            with self.assertRaisesRegex(host.Refusal, "collision"):
                runner.run()
        self.assertEqual((self.lock / "owner").read_text(), "new-owner\n")

    def test_migration_budget_is_exact_and_bound_to_the_original_config(self):
        self.assertEqual(self.runner().binding["migrationTimeoutSeconds"], 1200)
        for value in (None, True, 0, 59, 1801, "1200"):
            with self.subTest(value=value):
                request = copy.deepcopy(self.request)
                request["hostEvidence"]["migrationTimeoutSeconds"] = value
                with self.assertRaisesRegex(host.Refusal, "migration-budget"):
                    FakeHost(request, self.root)
        changed = dict(self.config, migrationTimeoutSeconds=1199)
        (self.migration / "config.json").write_text(json.dumps(changed))
        with self.assertRaisesRegex(host.Refusal, "config-migrationTimeoutSeconds"):
            self.runner().run()

    @unittest.skipUnless(os.name == "posix", "POSIX mode bits")
    def test_world_writable_owner_proof_never_releases(self):
        self.lock.chmod(0o777)
        (self.lock / "owner").chmod(0o666)
        with self.assertRaisesRegex(host.Refusal, "writable-retained-state"):
            self.release()
        self.assertTrue(self.lock.exists())
        self.assertFalse(self.archive.exists())

    @unittest.skipUnless(os.name == "posix", "POSIX private-group directory permissions")
    def test_private_775_deploy_directory_does_not_relax_retained_permissions(self):
        uid, gid = os.geteuid(), os.getegid()
        account = SimpleNamespace(pw_uid=uid, pw_gid=gid, pw_name="fixture")
        group = SimpleNamespace(gr_gid=gid, gr_name="fixture", gr_mem=[])
        (self.root / "deploy").chmod(0o775)
        with patch.dict(sys.modules, {
            "pwd": SimpleNamespace(getpwuid=lambda _: account, getpwall=lambda: [account]),
            "grp": SimpleNamespace(getgrgid=lambda _: group),
        }):
            self.assertEqual(self.runner().run()["lockState"], "owned")
            for path in (self.root, self.lock, self.migration, self.lock / "owner",
                         self.migration / "config.json", self.migration / "evidence.json"):
                with self.subTest(path=path.name):
                    original = path.stat().st_mode
                    path.chmod(original | 0o020)
                    with self.assertRaisesRegex(host.Refusal, "writable-retained-state"):
                        self.release()
                    path.chmod(original)
                    self.assertFalse(self.archive.exists())
            self.assertEqual(self.release()["lockState"], "archived")

    @unittest.skipUnless(sys.platform.startswith("linux"), "Linux POSIX access ACLs")
    def test_named_user_and_group_access_acls_keep_the_private_775_owner_locked(self):
        uid, gid = os.geteuid(), os.getegid()
        account = SimpleNamespace(pw_uid=uid, pw_gid=gid, pw_name="fixture")
        group = SimpleNamespace(gr_gid=gid, gr_name="fixture", gr_mem=[])
        deploy = self.root / "deploy"
        undefined = 0xffffffff
        with patch.dict(sys.modules, {
            "pwd": SimpleNamespace(getpwuid=lambda _: account, getpwall=lambda: [account]),
            "grp": SimpleNamespace(getgrgid=lambda _: group),
        }):
            # Linux's version-2 ACL xattr: owner, optional named user, owning
            # group, optional named group, effective mask, and other. Both
            # named entries retain mode 0775 yet add an independent writer.
            for named_tag in (0x02, 0x08):
                with self.subTest(named="user" if named_tag == 0x02 else "group"):
                    entries = [(0x01, 7, undefined), (0x04, 7, undefined),
                               (named_tag, 7, 4242), (0x10, 7, undefined), (0x20, 5, undefined)]
                    acl = struct.pack("<I", 2) + b"".join(struct.pack("<HHI", *entry) for entry in sorted(entries))
                    os.setxattr(deploy, "system.posix_acl_access", acl, follow_symlinks=False)
                    try:
                        self.assertEqual(host.stat.S_IMODE(deploy.stat().st_mode), 0o775)
                        self.assertEqual(os.getxattr(deploy, "system.posix_acl_access"), acl)
                        with self.assertRaisesRegex(host.Refusal, "untrusted-deploy-directory-acl"):
                            self.release()
                        self.assertEqual({path.name for path in self.lock.iterdir()}, {"owner"})
                        self.assertFalse(self.archive.exists())
                    finally:
                        os.removexattr(deploy, "system.posix_acl_access")

    @unittest.skipUnless(os.name == "posix", "POSIX directory ACL gate")
    def test_acl_enumeration_failure_keeps_the_original_owner_locked(self):
        uid, gid = os.geteuid(), os.getegid()
        account = SimpleNamespace(pw_uid=uid, pw_gid=gid, pw_name="fixture")
        group = SimpleNamespace(gr_gid=gid, gr_name="fixture", gr_mem=[])
        (self.root / "deploy").chmod(0o775)
        with patch.dict(sys.modules, {
            "pwd": SimpleNamespace(getpwuid=lambda _: account, getpwall=lambda: [account]),
            "grp": SimpleNamespace(getgrgid=lambda _: group),
        }), patch.object(host.os, "listxattr", side_effect=PermissionError("ACL listing denied")):
            with self.assertRaisesRegex(host.Refusal, "unavailable-deploy-directory-acl"):
                self.release()
        self.assertEqual({path.name for path in self.lock.iterdir()}, {"owner"})
        self.assertFalse(self.archive.exists())

    def test_loss_before_rename_retries_only_immutable_same_proof(self):
        with patch.object(host.os, "rename", side_effect=OSError("disconnect")):
            with self.assertRaises(OSError):
                self.release()
        receipt = (self.lock / "reconciliation.json").read_bytes()
        self.request["reconciliationRunId"] = "300"
        self.runner().run()
        self.assertEqual(receipt, (self.archive / "reconciliation.json").read_bytes())

    def test_retry_reestablishes_durability_after_each_interrupted_sync(self):
        # Separate fixtures are needed for each failure point, because release
        # retains the original owner archive and never deletes recovery proof.
        for failure in ("file", "lock-directory", "renamed-directory"):
            with self.subTest(failure=failure):
                with tempfile.TemporaryDirectory() as temporary:
                    root = Path(temporary).resolve()
                    shutil.copytree(self.root, root, dirs_exist_ok=True)
                    request = dict(self.request, mode="release", preparedArtifact=self.prepared)
                    runner = FakeHost(request, root)
                    if failure == "file":
                        with patch.object(host.os, "fsync", side_effect=OSError("power loss")):
                            with self.assertRaises(OSError):
                                runner.run()
                    else:
                        boundary = runner.lock if failure == "lock-directory" else root
                        def fail_sync(path):
                            if path == boundary:
                                raise OSError("power loss")
                        with patch.object(runner, "sync_directory", side_effect=fail_sync):
                            with self.assertRaises(OSError):
                                runner.run()
                    retry = FakeHost(request, root)
                    locations = []
                    with patch.object(retry, "sync_directory", side_effect=locations.append), \
                            patch.object(retry, "sync_receipt", wraps=retry.sync_receipt) as receipt_sync:
                        result = retry.run()
                    self.assertEqual(result["lockState"], "archived")
                    self.assertEqual(receipt_sync.call_count, 1)
                    self.assertEqual(locations[-1], root)
                    self.assertEqual(locations[0], retry.archive if failure == "renamed-directory" else retry.lock)

    def test_published_archive_cannot_authorize_a_new_lock(self):
        self.release()
        self.lock.mkdir()
        (self.lock / "owner").write_text("new-owner\n")
        with self.assertRaisesRegex(host.Refusal, "collision"):
            self.runner().run()
        self.assertEqual((self.lock / "owner").read_text(), "new-owner\n")

    def test_absent_lock_is_never_blindly_adopted(self):
        (self.lock / "owner").unlink()
        self.lock.rmdir()
        with self.assertRaisesRegex(host.Refusal, "missing-owner-proof"):
            self.runner().run()
        self.archive.mkdir()
        (self.archive / "owner").write_bytes(("a" * 32 + "\n").encode())
        with self.assertRaisesRegex(host.Refusal, "archive-without-receipt"):
            self.runner().run()

    def test_foreign_and_partial_receipts_fail_closed(self):
        (self.lock / "reconciliation.json").write_text('{"incomplete":')
        with self.assertRaises(ValueError):
            self.release()
        self.assertTrue(self.lock.exists())
        (self.lock / "reconciliation.json").unlink()
        self.release()
        self.request["preparedArtifact"] = dict(self.prepared, id=999)
        with self.assertRaisesRegex(host.Refusal, "prepared-mismatch"):
            self.runner().run()
        self.request["sourceRunId"] = "999"
        with self.assertRaisesRegex(host.Refusal, "receipt-binding"):
            self.runner().run()

    def test_unsafe_extra_files_and_links_fail_closed(self):
        (self.lock / "extra").write_text("leave me")
        with self.assertRaisesRegex(host.Refusal, "lock-contents"):
            self.release()
        (self.lock / "extra").unlink()
        if os.name == "posix":
            original = self.lock / "owner"
            original.rename(self.root / "other")
            original.symlink_to(self.root / "other")
            with self.assertRaisesRegex(host.Refusal, "unsafe-file"):
                self.release()

    def test_hardlinked_evidence_and_receipt_are_not_accepted(self):
        for relative in ("deploy/.migration-" + "a" * 32 + "/evidence.json",
                         "deploy/.migration-" + "a" * 32 + "/config.json", ".deploy-lock/owner"):
            with self.subTest(path=relative), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                shutil.copytree(self.root, root, dirs_exist_ok=True)
                os.link(root / relative, root / "hardlink")
                with self.assertRaisesRegex(host.Refusal, "unsafe-file"):
                    FakeHost(self.request, root).run()
        self.release()
        os.link(self.archive / "reconciliation.json", self.root / "hardlink")
        with self.assertRaisesRegex(host.Refusal, "unsafe-file"):
            self.runner().run()

    def test_posix_namespace_links_cannot_redirect_ownership(self):
        # Directory/dangling symlinks are host-side POSIX cases. Native Windows
        # fixtures do not require symlink privilege; Linux CI exercises them.
        if os.name != "posix":
            return
        for relative in (".", "deploy", "deploy/.migration-" + "a" * 32, ".deploy-lock"):
            with self.subTest(path=relative), tempfile.TemporaryDirectory() as temporary:
                parent = Path(temporary).resolve()
                root = parent / "root"
                shutil.copytree(self.root, root)
                source = root / relative
                if relative == ".":
                    source = root
                destination = parent / "redirected"
                source.rename(destination)
                source.symlink_to(destination, target_is_directory=True)
                with self.assertRaises(host.Refusal):
                    FakeHost(self.request, root).run()
        for relative in (".deploy-reconciled-" + "a" * 32, ".csx-deploy-aborted-" + "a" * 32):
            with self.subTest(path=relative):
                path = self.root / relative
                path.symlink_to(self.root / "does-not-exist")
                with self.assertRaises(host.Refusal):
                    self.runner().run()
                path.unlink()

    def test_duplicate_keys_and_diagnostic_privacy(self):
        with self.assertRaises(host.Refusal):
            host.strict_json('{"owner":"a","owner":"b"}')
        request = dict(self.request, mode="secret-must-never-appear")
        encoded = base64.b64encode(json.dumps(request).encode()).decode()
        result = subprocess.run([sys.executable, "-B", str(Path(__file__).with_name("reconcile-host.py")), encoded],
                                capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertEqual(result.stderr.strip(), "CSX-RECONCILE-REFUSED mode")


class PrivateDirectoryACLTests(unittest.TestCase):
    def test_no_access_acl_allows_empty_or_unrelated_attributes(self):
        for attributes in ([], ["user.audit-note"]):
            with self.subTest(attributes=attributes), \
                    patch.object(host.os, "listxattr", return_value=attributes, create=True) as read:
                host.verify_no_access_acl("deploy-fixture")
                read.assert_called_once_with("deploy-fixture", follow_symlinks=False)

    def test_any_extended_access_acl_is_refused(self):
        with patch.object(host.os, "listxattr", return_value=["system.posix_acl_access"], create=True):
            with self.assertRaisesRegex(host.Refusal, "untrusted-deploy-directory-acl"):
                host.verify_no_access_acl("deploy-fixture")

    def test_attribute_read_errors_are_not_treated_as_absence(self):
        for error in (PermissionError("denied"), OSError("unsupported attributes"), OSError("I/O failure")):
            with self.subTest(error=error), patch.object(host.os, "listxattr", side_effect=error, create=True):
                with self.assertRaisesRegex(host.Refusal, "unavailable-deploy-directory-acl"):
                    host.verify_no_access_acl("deploy-fixture")


class PrivateDirectoryPermissionsTests(unittest.TestCase):
    def setUp(self):
        self.account = SimpleNamespace(pw_uid=1000, pw_gid=1000, pw_name="ubuntu")
        self.accounts = [SimpleNamespace(pw_uid=0, pw_gid=0, pw_name="root"), self.account]
        self.group = SimpleNamespace(gr_gid=1000, gr_name="ubuntu", gr_mem=[])
        self.info = SimpleNamespace(st_mode=host.stat.S_IFDIR | 0o775, st_uid=1000, st_gid=1000)
        self.addCleanup(patch.stopall)
        patch.object(host.os, "geteuid", return_value=1000, create=True).start()
        patch.object(host.os, "getegid", return_value=1000, create=True).start()
        patch.dict(sys.modules, {
            "pwd": SimpleNamespace(getpwuid=lambda _: self.account, getpwall=lambda: self.accounts),
            "grp": SimpleNamespace(getgrgid=lambda _: self.group),
        }).start()

    def test_exact_owner_private_primary_group_has_no_additional_writer(self):
        host.verify_permissions(self.info, private_group_directory=True)
        self.group.gr_mem = ["ubuntu", "root"]
        host.verify_permissions(self.info, private_group_directory=True)

    def test_exception_never_applies_to_retained_state_or_files(self):
        with self.assertRaisesRegex(host.Refusal, "writable-retained-state"):
            host.verify_permissions(self.info)
        self.info.st_mode = host.stat.S_IFREG | 0o664
        with self.assertRaisesRegex(host.Refusal, "writable-retained-state"):
            host.verify_permissions(self.info, private_group_directory=True)

    def test_world_write_foreign_owner_gid_and_special_modes_are_refused(self):
        for key, value in (("st_uid", 2000), ("st_gid", 2000),
                           ("st_mode", host.stat.S_IFDIR | 0o777),
                           ("st_mode", host.stat.S_IFDIR | 0o2775)):
            with self.subTest(field=key, value=value):
                original = getattr(self.info, key)
                setattr(self.info, key, value)
                with self.assertRaises(host.Refusal):
                    host.verify_permissions(self.info, private_group_directory=True)
                setattr(self.info, key, original)

    def test_other_supplementary_or_primary_group_members_are_refused(self):
        self.group.gr_mem = ["other"]
        with self.assertRaisesRegex(host.Refusal, "untrusted-deploy-directory-group"):
            host.verify_permissions(self.info, private_group_directory=True)
        self.group.gr_mem = []
        self.accounts.append(SimpleNamespace(pw_uid=2000, pw_gid=1000, pw_name="other"))
        with self.assertRaisesRegex(host.Refusal, "untrusted-deploy-directory-group"):
            host.verify_permissions(self.info, private_group_directory=True)

    def test_nonprivate_group_or_incomplete_account_enumeration_is_refused(self):
        self.group.gr_name = "shared"
        with self.assertRaisesRegex(host.Refusal, "untrusted-deploy-directory-group"):
            host.verify_permissions(self.info, private_group_directory=True)
        self.group.gr_name = "ubuntu"
        self.accounts = []
        with self.assertRaisesRegex(host.Refusal, "untrusted-deploy-directory-group"):
            host.verify_permissions(self.info, private_group_directory=True)


if __name__ == "__main__":
    unittest.main()
