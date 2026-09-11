import base64
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("controller", ROOT / "reconcile-production.py")
controller = importlib.util.module_from_spec(spec)
spec.loader.exec_module(controller)


class ControllerTests(unittest.TestCase):
    def setUp(self):
        self.expected = {
            "schemaVersion": 1, "owner": "a" * 32, "operationalSha": "b" * 40,
            "targetSha": "c" * 40, "previousSha": "d" * 40,
            "imageDigest": "sha256:" + "e" * 64, "previousImageDigest": "sha256:" + "f" * 64,
            "expectedReleaseTag": "v0.1.158", "expectedMigration": "0037_slow_query_indexes.sql",
            "expectedMigrationCount": 38, "migrationTimeoutSeconds": 1200,
            "serverStartedAt": "2026-09-11T14:15:58.802547123Z", "evidenceSha256": "1" * 64,
        }
        self.report = dict(self.expected, conclusion="success", lockDisposition="retained",
                           acceptanceAuthority="host-reconciliation", health="ok", proxyHealth="ok",
                           smoke="pass", representativeSmoke="pass", cleanup="pass",
                           servedRevision=self.expected["targetSha"], retainedEvidenceSha256="1" * 64,
                           checks={stage: "pass" for stage in (
                               "retained", "supervisor-before", "identity-before", "database", "health-smoke",
                               "identity-after", "cleanup-after", "supervisor-after", "retained-stable")})
        identity = {"imageDigest": self.expected["imageDigest"], "configuredRevision": "c" * 40,
                    "serverStartedAt": self.expected["serverStartedAt"], "releaseTag": "v0.1.158", "restartCount": 0}
        supervisor = {"Id": "csx-migration-" + "a" * 32 + ".service", "ActiveState": "inactive",
                      "SubState": "dead", "MainPID": "0", "ControlPID": "0", "LoadState": "not-found", "ControlGroup": ""}
        self.report.update(identityBefore=identity, identityAfter=identity,
                           supervisorBefore=supervisor, supervisorAfter=supervisor,
                           retainedConfigSha256="2" * 64,
                           migrationLedger={"version": "0037_slow_query_indexes.sql", "count": 38},
                           indexes=[{"name": name, "valid": True, "ready": True, "definition": definition}
                                    for name, definition in controller.validation.INDEXES.items()])
        self.args = types.SimpleNamespace(host="192.0.2.1", user="ubuntu", key="fixture-key",
                                          known_hosts="fixture-hosts")

    def test_strict_host_identity_and_complete_checks(self):
        controller.validate_host_report(self.report, self.expected)
        for key in self.expected:
            if key == "schemaVersion":
                continue
            with self.subTest(key=key):
                broken = dict(self.report)
                broken.pop(key)
                with self.assertRaises(RuntimeError):
                    controller.validate_host_report(broken, self.expected)
        for key in ("conclusion", "lockDisposition", "acceptanceAuthority", "checks", "health",
                    "proxyHealth", "smoke", "representativeSmoke", "cleanup", "retainedEvidenceSha256"):
            with self.subTest(key=key):
                with self.assertRaises(RuntimeError):
                    controller.validate_host_report(dict(self.report, **{key: "wrong"}), self.expected)

    def test_nonzero_ssh_cannot_publish_valid_looking_acceptance(self):
        response = subprocess.CompletedProcess([], 1, json.dumps(self.report).encode(), b"sensitive stderr")
        with patch.object(controller.subprocess, "run", return_value=response):
            with self.assertRaisesRegex(RuntimeError, "host-verification-failed:unavailable"):
                controller.collect_host(self.args, self.expected)

    def test_pinned_bounded_ssh_and_exact_json_timestamp(self):
        response = subprocess.CompletedProcess([], 0, json.dumps(self.report).encode(), b"")
        with patch.object(controller.subprocess, "run", return_value=response) as run:
            result = controller.collect_host(self.args, self.expected)
        self.assertEqual(result["serverStartedAt"], self.expected["serverStartedAt"])
        command = run.call_args.args[0]
        self.assertIn("StrictHostKeyChecking=yes", command)
        self.assertIn("BatchMode=yes", command)
        self.assertIn("UserKnownHostsFile=fixture-hosts", command)
        self.assertEqual(run.call_args.kwargs["timeout"], 255)
        self.assertIn("timeout --signal=TERM --kill-after=5 240 python3 -", command)
        self.assertNotIn("fixture-key", run.call_args.kwargs["input"].decode())

    def test_remote_failure_is_redacted_to_fixed_stage(self):
        response = subprocess.CompletedProcess([], 1, json.dumps({
            "failureStage": "health-smoke", "failureCode": "secret://private-data"
        }).encode(), b"sensitive stderr")
        with patch.object(controller.subprocess, "run", return_value=response):
            with self.assertRaisesRegex(RuntimeError, "^host-verification-failed:health-smoke$"):
                controller.collect_host(self.args, self.expected)

    def test_actual_streamed_bundle_refuses_before_host_io(self):
        # Empty expectations stop before constructing a Host on every platform.
        result = subprocess.run([sys.executable, "-"], input=controller.host_program({}).encode(),
                                capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 1, result.stderr.decode())
        report = json.loads(result.stdout)
        self.assertEqual(report["conclusion"], "failure")
        self.assertEqual(report["failureStage"], "expected")

    def test_public_tls_health_and_revision_are_required(self):
        good = [subprocess.CompletedProcess([], 0, b"ok", b""),
                subprocess.CompletedProcess([], 0, json.dumps({"version": "v0.1.158",
                                            "revision": "c" * 40}).encode(), b"")]
        with patch.object(controller.subprocess, "run", side_effect=good) as run:
            self.assertEqual(controller.public_probe("192.0.2.1", self.expected)["healthz"], "ok")
        for call in run.call_args_list:
            command = call.args[0]
            self.assertIn("--fail", command)
            self.assertIn("--max-time", command)
            self.assertIn("codesamplex.dev:443:192.0.2.1", command)
            self.assertNotIn("--insecure", command)
            self.assertNotIn("-k", command)
        for response in (subprocess.CompletedProcess([], 22, b"ok", b""),
                         subprocess.CompletedProcess([], 0, b"database unavailable", b"")):
            with patch.object(controller.subprocess, "run", return_value=response):
                with self.assertRaises(RuntimeError):
                    controller.public_probe("192.0.2.1", self.expected)
        stale = subprocess.CompletedProcess([], 0, b'{"version":"v0.1.158","revision":"stale"}', b"")
        with patch.object(controller.subprocess, "run", side_effect=[good[0], stale]):
            with self.assertRaisesRegex(RuntimeError, "public-version-mismatch"):
                controller.public_probe("192.0.2.1", self.expected)

    def test_failed_post_probe_never_publishes_success(self):
        provenance = {"trackingIssue": "347", "originalDeploymentRunId": "123",
                      "originalDeploymentRunNumber": 76, "originalArtifactId": "456",
                      "originalArtifactSha256": "2" * 64, "originalEvidenceSha256": "3" * 64}
        for failure_at in ("before", "after", "none"):
            with self.subTest(failure_at=failure_at), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "expected.json").write_text(json.dumps(self.expected))
                (root / "provenance.json").write_text(json.dumps(provenance))
                argv = ["reconcile-production.py", "--expected", str(root / "expected.json"),
                        "--provenance", str(root / "provenance.json"), "--output", str(root / "out.json"),
                        "--host", "192.0.2.1", "--key", "fixture", "--known-hosts", "fixture"]
                probes = [RuntimeError("public-healthz-unavailable")] if failure_at == "before" else [
                    {}, RuntimeError("public-healthz-unavailable") if failure_at == "after" else {}]
                with patch.object(sys, "argv", argv), patch.dict(controller.os.environ, {
                    "GITHUB_RUN_ID": "789", "GITHUB_SHA": "4" * 40
                }), patch.object(controller, "public_probe", side_effect=probes), patch.object(
                        controller, "collect_host", return_value=self.report) as host:
                    status = controller.main()
                evidence = json.loads((root / "out.json").read_text())
                self.assertEqual(evidence["reconciliation"]["lockDisposition"], "retained")
                self.assertEqual(status, 0 if failure_at == "none" else 1)
                self.assertEqual(evidence["conclusion"], "success" if status == 0 else "failure")
                if failure_at == "before":
                    host.assert_not_called()
                if status:
                    self.assertEqual(evidence["deployedSha"], "")


if __name__ == "__main__":
    unittest.main()
