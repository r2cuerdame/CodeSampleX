#!/usr/bin/env python3
"""Regression and provenance tests for pre-activation deploy lock recovery."""
import base64
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch, Mock
import zipfile

ROOT = Path(__file__).resolve().parent


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    val = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(val)
    return val


runner = module("recover_runner_module", "recover-deploy-lock.py")
provenance = runner.provenance
host = module("recover_host_module", "recover-deploy-lock-host.py")

REPO = "r2cuerdame/CodeSampleX"
FAILED_RUN_ID = 34831402445
ARTIFACT_ID = 10342780896
TARGET_SHA = "c45f928d1ab14abc55d3641c08f72d848ef49ce2"
PREV_SHA = "dea13af9c0d5dd4ed1ef06b55f46f123be1c0069"
PREV_IMAGE = "sha256:d9960286827f50a3892a755ae0b23eca5153a60087c966a28f4e15bccbd5ce34"
OPERATIONAL_SHA = "b9d3af73d96671576b888b8c61494dd2b0cf2ea2"
OWNER_TOKEN = "78a5e9f14309489ca6b541334c45e89d"


def raw_json(val):
    return (json.dumps(val, indent=2) + "\n").encode("utf-8")


def source_run_fixture():
    return {
        "id": FAILED_RUN_ID,
        "path": ".github/workflows/production-deploy.yml",
        "event": "workflow_dispatch",
        "status": "completed",
        "conclusion": "failure",
        "head_branch": "main",
        "head_sha": OPERATIONAL_SHA,
        "run_number": 88,
        "run_attempt": 1,
        "repository": {"full_name": REPO},
        "head_repository": {"full_name": REPO},
    }


def jobs_fixture():
    return [
        {
            "name": "Production eligibility",
            "run_id": FAILED_RUN_ID,
            "run_attempt": 1,
            "status": "completed",
            "conclusion": "success",
            "started_at": "2026-09-14T10:05:00Z",
            "completed_at": "2026-09-14T10:05:25Z",
            "steps": [{"name": "Eligibility check", "conclusion": "success"}],
        },
        {
            "name": "Roll out production",
            "run_id": FAILED_RUN_ID,
            "run_attempt": 1,
            "status": "completed",
            "conclusion": "failure",
            "started_at": "2026-09-14T10:05:30Z",
            "completed_at": "2026-09-14T10:11:50Z",
            "steps": [{"name": "Deploy and verify", "conclusion": "failure"}],
        },
    ]


def evidence_fixture():
    return {
        "schemaVersion": 2,
        "conclusion": "failure",
        "failureClass": "rollback-critical",
        "rollback": "unverified",
        "workflowRunId": str(FAILED_RUN_ID),
        "operationalSha": OPERATIONAL_SHA,
        "targetSha": TARGET_SHA,
        "previousProductionSha": PREV_SHA,
        "previousImageDigest": PREV_IMAGE,
        "deployedSha": PREV_SHA,
        "servedRevision": PREV_SHA,
        "imageDigest": PREV_IMAGE,
        "offlineMigration": None,
        "serverStartedAt": "",
        "health": "ok",
        "smoke": "not-started",
        "trackingIssue": "406",
    }


def make_artifact_zip(files):
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", zipfile.ZIP_DEFLATED) as z:
        for name, content in files.items():
            z.writestr(name, content)
    data = stream.getvalue()
    digest = "sha256:" + provenance.digest(data)
    meta = {
        "id": ARTIFACT_ID,
        "name": f"production-evidence-{FAILED_RUN_ID}",
        "expired": False,
        "created_at": "2026-09-14T10:11:46Z",
        "digest": digest,
        "workflow_run": {
            "id": FAILED_RUN_ID,
            "head_sha": OPERATIONAL_SHA,
            "head_branch": "main",
        },
    }
    return meta, data


class MockGitHub:
    def __init__(self):
        self.repository = REPO
        self.endpoints = {}
        self.pages_data = {}

    def api(self, path, binary=False):
        if path not in self.endpoints:
            raise ValueError(f"unknown api path: {path}")
        return copy.deepcopy(self.endpoints[path])

    def pages(self, path, key):
        if (path, key) not in self.pages_data:
            raise ValueError(f"unknown pages path: {path}, {key}")
        return copy.deepcopy(self.pages_data[(path, key)])


class FakeHostRunner(host.RecoverHost):
    def __init__(self, request, root):
        super().__init__(request, root)
        self.commands_run = []
        self.command_overrides = {}

    def command(self, args, seconds=10):
        self.commands_run.append(args)
        cmd_str = " ".join(args)
        for pat, resp in self.command_overrides.items():
            if pat in cmd_str:
                if isinstance(resp, Exception):
                    raise resp
                return resp

        # Default success responses
        if args[0] == "systemctl" and args[1] == "show":
            return "ActiveState=inactive\nSubState=dead\nMainPID=0\nControlPID=0\n"
        if args[0] == "systemctl" and args[1] == "list-units":
            return ""
        if args[0] == "docker" and args[1] == "ps":
            return ""
        if "psql" in cmd_str:
            return '{"owned":0,"ddl":0}'
        if args[0] == "ps":
            return "1 /bin/init\n100 python3\n"
        if args[0] == "docker" and args[1:3] == ["inspect", "codesamplex-server-1"]:
            return json.dumps([{
                "Id": "container-dea13af9c0d5",
                "Image": self.request["previousImageDigest"],
                "Config": {"Env": ["CSX_VERSION=" + self.request["previousProductionSha"]]},
                "State": {"Running": True, "OOMKilled": False, "StartedAt": "2026-09-14T09:00:00Z"},
                "RestartCount": 0
            }])
        if args[0] == "docker" and args[1:4] == ["image", "inspect", self.request["previousImageDigest"]]:
            return json.dumps([{
                "Id": self.request["previousImageDigest"],
                "Config": {"Labels": {"org.opencontainers.image.revision": self.request["previousProductionSha"]}}
            }])
        if "wget" in cmd_str:
            if "healthz" in cmd_str:
                return "ok\n"
            return json.dumps({"revision": self.request["previousProductionSha"]})
        if "curl" in cmd_str:
            if "healthz" in cmd_str:
                return "ok\n\n200"
            return json.dumps({"revision": self.request["previousProductionSha"]}) + "\n\n200"

        return ""


class TestRecoverProvenanceAndRunner(unittest.TestCase):
    def setUp(self):
        self.api = MockGitHub()
        self.run = source_run_fixture()
        self.jobs = jobs_fixture()
        self.evidence = evidence_fixture()
        self.meta, self.zip_bytes = make_artifact_zip({provenance.TOP: raw_json(self.evidence)})

        self.api.endpoints[f"actions/runs/{FAILED_RUN_ID}/attempts/1"] = self.run
        self.api.pages_data[(f"actions/runs/{FAILED_RUN_ID}/attempts/1/jobs", "jobs")] = self.jobs
        self.api.endpoints[f"actions/artifacts/{ARTIFACT_ID}"] = self.meta
        self.api.endpoints[f"actions/artifacts/{ARTIFACT_ID}/zip"] = self.zip_bytes

        ci_runs = {
            "workflow_runs": [{
                "path": ".github/workflows/ci.yml",
                "head_branch": "main",
                "event": "push",
                "conclusion": "success"
            }]
        }
        self.api.endpoints[f"actions/workflows/ci.yml/runs?head_sha={OPERATIONAL_SHA}&branch=main&status=success&per_page=100"] = ci_runs

    def test_successful_source_authentication(self):
        run, rollout = runner.authenticate_source_run(self.api, FAILED_RUN_ID, 1)
        self.assertEqual(run["id"], FAILED_RUN_ID)
        artifact, raw_zip, top, top_bytes = runner.authenticate_source_artifact(self.api, run, rollout, ARTIFACT_ID)
        self.assertEqual(artifact["id"], ARTIFACT_ID)
        runner.validate_preactivation_evidence(run, top, REPO)
        runner.validate_operational_ci(self.api, OPERATIONAL_SHA)

    def test_refuses_non_deploy_workflow(self):
        self.run["path"] = ".github/workflows/ci.yml"
        self.api.endpoints[f"actions/runs/{FAILED_RUN_ID}/attempts/1"] = self.run
        with self.assertRaises(ValueError):
            runner.authenticate_source_run(self.api, FAILED_RUN_ID, 1)

    def test_refuses_non_main_branch(self):
        self.run["head_branch"] = "feature"
        self.api.endpoints[f"actions/runs/{FAILED_RUN_ID}/attempts/1"] = self.run
        with self.assertRaises(ValueError):
            runner.authenticate_source_run(self.api, FAILED_RUN_ID, 1)

    def test_refuses_successful_conclusion(self):
        self.run["conclusion"] = "success"
        self.api.endpoints[f"actions/runs/{FAILED_RUN_ID}/attempts/1"] = self.run
        with self.assertRaises(ValueError):
            runner.authenticate_source_run(self.api, FAILED_RUN_ID, 1)

    def test_refuses_artifact_id_mismatch(self):
        run, rollout = runner.authenticate_source_run(self.api, FAILED_RUN_ID, 1)
        with self.assertRaises(ValueError):
            runner.authenticate_source_artifact(self.api, run, rollout, 99999999)

    def test_refuses_expired_artifact(self):
        self.meta["expired"] = True
        self.api.endpoints[f"actions/artifacts/{ARTIFACT_ID}"] = self.meta
        run, rollout = runner.authenticate_source_run(self.api, FAILED_RUN_ID, 1)
        with self.assertRaises(ValueError):
            runner.authenticate_source_artifact(self.api, run, rollout, ARTIFACT_ID)

    def test_refuses_artifact_with_extra_members(self):
        # Existing committed-owner reconciliation included migration json; pre-activation MUST NOT
        meta, zbytes = make_artifact_zip({
            provenance.TOP: raw_json(self.evidence),
            provenance.HOST: b"{}"
        })
        self.api.endpoints[f"actions/artifacts/{ARTIFACT_ID}"] = meta
        self.api.endpoints[f"actions/artifacts/{ARTIFACT_ID}/zip"] = zbytes
        run, rollout = runner.authenticate_source_run(self.api, FAILED_RUN_ID, 1)
        with self.assertRaises(ValueError):
            runner.authenticate_source_artifact(self.api, run, rollout, ARTIFACT_ID)

    def test_refuses_active_migration_evidence(self):
        self.evidence["offlineMigration"] = {"unit": "csx-migration.service"}
        with self.assertRaises(ValueError):
            runner.validate_preactivation_evidence(self.run, self.evidence, REPO)

    def test_refuses_non_empty_server_started_at(self):
        self.evidence["serverStartedAt"] = "2026-09-14T10:10:00Z"
        with self.assertRaises(ValueError):
            runner.validate_preactivation_evidence(self.run, self.evidence, REPO)

    def test_refuses_deployed_sha_mutation(self):
        self.evidence["deployedSha"] = TARGET_SHA
        with self.assertRaises(ValueError):
            runner.validate_preactivation_evidence(self.run, self.evidence, REPO)

    def test_refuses_image_mutation(self):
        self.evidence["imageDigest"] = "sha256:" + "0" * 64
        with self.assertRaises(ValueError):
            runner.validate_preactivation_evidence(self.run, self.evidence, REPO)

    def test_refuses_unsuccessful_main_ci(self):
        self.api.endpoints[f"actions/workflows/ci.yml/runs?head_sha={OPERATIONAL_SHA}&branch=main&status=success&per_page=100"] = {
            "workflow_runs": [{"path": ".github/workflows/ci.yml", "conclusion": "failure"}]
        }
        with self.assertRaises(ValueError):
            runner.validate_operational_ci(self.api, OPERATIONAL_SHA)

    @patch.object(runner, "public_health")
    @patch.object(runner, "validate_ancestry")
    @patch.object(runner, "remote")
    def test_runner_main_verify_mode(self, mock_remote, mock_ancestry, mock_health):
        mock_remote.return_value = {
            "schemaVersion": 1,
            "recoveryClass": "pre-activation-retained-lock",
            "verifiedAt": "2026-09-14T10:30:00Z",
            "owner": OWNER_TOKEN,
            "archive": f"/opt/codesamplex/.deploy-lock.recovered-{FAILED_RUN_ID}-{OWNER_TOKEN}",
            "lockState": "owned",
            "health": "ok",
            "containerId": "container-dea13af9c0d5",
            "containerStartedAt": "2026-09-14T09:00:00Z",
            "receipt": None,
        }
        with tempfile.NamedTemporaryFile(suffix=".json", delete=False) as out_file:
            out_path = out_file.name

        try:
            with patch.dict(os.environ, {
                "GITHUB_REPOSITORY": REPO,
                "GITHUB_SHA": OPERATIONAL_SHA,
                "GITHUB_RUN_ID": "500",
                "GITHUB_RUN_ATTEMPT": "1",
                "PRODUCTION_HOST": "prod.example.com",
                "PRODUCTION_USER": "ubuntu",
                "PRODUCTION_KEY_PATH": str(ROOT / "deploy.ps1"),
                "PRODUCTION_KNOWN_HOSTS_PATH": str(ROOT / "deploy.ps1"),
            }), patch("sys.argv", [
                "recover-deploy-lock.py", "verify",
                "--source-run-id", str(FAILED_RUN_ID),
                "--source-run-attempt", "1",
                "--source-artifact-id", str(ARTIFACT_ID),
                "--output", out_path,
            ]), patch.object(runner.provenance, "GitHub", return_value=self.api):
                runner.main(api=self.api)

            evidence = json.loads(Path(out_path).read_text(encoding="utf-8"))
            self.assertEqual(evidence["conclusion"], "success")
            self.assertEqual(evidence["recovery"]["verification"]["lockState"], "owned")
            self.assertEqual(evidence["previousProductionSha"], PREV_SHA)
            self.assertEqual(evidence["targetSha"], TARGET_SHA)
        finally:
            if os.path.exists(out_path):
                os.unlink(out_path)

    @patch.object(runner, "public_health")
    @patch.object(runner, "validate_ancestry")
    @patch.object(runner, "remote")
    def test_runner_main_release_mode(self, mock_remote, mock_ancestry, mock_health):
        mock_remote.return_value = {
            "schemaVersion": 1,
            "recoveryClass": "pre-activation-retained-lock",
            "verifiedAt": "2026-09-14T10:30:00Z",
            "owner": OWNER_TOKEN,
            "archive": f"/opt/codesamplex/.deploy-lock.recovered-{FAILED_RUN_ID}-{OWNER_TOKEN}",
            "lockState": "archived",
            "health": "ok",
            "containerId": "container-dea13af9c0d5",
            "containerStartedAt": "2026-09-14T09:00:00Z",
            "receipt": {
                "schemaVersion": 1,
                "recoveryClass": "pre-activation-retained-lock",
                "owner": OWNER_TOKEN,
                "sourceRunId": str(FAILED_RUN_ID),
                "sourceArtifactId": ARTIFACT_ID,
            },
        }
        with tempfile.NamedTemporaryFile(suffix=".json", delete=False) as out_file:
            out_path = out_file.name

        try:
            with patch.dict(os.environ, {
                "GITHUB_REPOSITORY": REPO,
                "GITHUB_SHA": OPERATIONAL_SHA,
                "GITHUB_RUN_ID": "500",
                "GITHUB_RUN_ATTEMPT": "1",
                "PRODUCTION_HOST": "prod.example.com",
                "PRODUCTION_USER": "ubuntu",
                "PRODUCTION_KEY_PATH": str(ROOT / "deploy.ps1"),
                "PRODUCTION_KNOWN_HOSTS_PATH": str(ROOT / "deploy.ps1"),
            }), patch("sys.argv", [
                "recover-deploy-lock.py", "release",
                "--source-run-id", str(FAILED_RUN_ID),
                "--source-run-attempt", "1",
                "--source-artifact-id", str(ARTIFACT_ID),
                "--output", out_path,
            ]), patch.object(runner.provenance, "GitHub", return_value=self.api):
                runner.main(api=self.api)

            evidence = json.loads(Path(out_path).read_text(encoding="utf-8"))
            self.assertEqual(evidence["conclusion"], "success")
            self.assertEqual(evidence["recovery"]["verification"]["lockState"], "archived")
            self.assertEqual(evidence["recovery"]["lockOwner"], OWNER_TOKEN)
        finally:
            if os.path.exists(out_path):
                os.unlink(out_path)


class TestRecoverHostVerification(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.deploy = self.root / "deploy"
        self.deploy.mkdir()
        self.lock = self.root / ".deploy-lock"
        self.lock.mkdir()
        (self.lock / "owner").write_text(OWNER_TOKEN + "\n", encoding="utf-8")

        self.req = {
            "mode": "verify",
            "repository": REPO,
            "sourceRunId": str(FAILED_RUN_ID),
            "sourceRunAttempt": 1,
            "sourceArtifactId": ARTIFACT_ID,
            "sourceArtifactSha256": "a" * 64,
            "sourceEvidenceSha256": "b" * 64,
            "targetSha": TARGET_SHA,
            "previousProductionSha": PREV_SHA,
            "previousImageDigest": PREV_IMAGE,
            "recoveryRunId": "500",
            "recoveryRunAttempt": 1,
            "operationalSha": OPERATIONAL_SHA,
        }

    def tearDown(self):
        self.temp_dir.cleanup()

    def test_verify_mode_success(self):
        host_runner = FakeHostRunner(self.req, self.root)
        res = host_runner.run()
        self.assertEqual(res["schemaVersion"], 1)
        self.assertEqual(res["recoveryClass"], "pre-activation-retained-lock")
        self.assertEqual(res["lockState"], "owned")
        self.assertEqual(res["owner"], OWNER_TOKEN)
        self.assertTrue(self.lock.exists())
        self.assertFalse(Path(res["archive"]).exists())

    def test_release_mode_atomically_archives_lock(self):
        self.req["mode"] = "release"
        host_runner = FakeHostRunner(self.req, self.root)
        res = host_runner.run()
        self.assertEqual(res["schemaVersion"], 1)
        self.assertEqual(res["lockState"], "archived")
        self.assertEqual(res["owner"], OWNER_TOKEN)
        self.assertFalse(self.lock.exists())
        archive = Path(res["archive"])
        self.assertTrue(archive.is_dir())
        self.assertEqual((archive / "owner").read_text(encoding="utf-8").strip(), OWNER_TOKEN)
        receipt = json.loads((archive / "recovery.json").read_text(encoding="utf-8"))
        self.assertEqual(receipt["owner"], OWNER_TOKEN)
        self.assertEqual(receipt["sourceRunId"], str(FAILED_RUN_ID))

    def test_idempotent_retry_after_archive(self):
        self.req["mode"] = "release"
        host_runner = FakeHostRunner(self.req, self.root)
        res1 = host_runner.run()
        self.assertEqual(res1["lockState"], "archived")

        # Second run should detect existing archive and return archived state cleanly
        host_runner2 = FakeHostRunner(self.req, self.root)
        res2 = host_runner2.run()
        self.assertEqual(res2["lockState"], "archived")
        self.assertEqual(res2["owner"], OWNER_TOKEN)

    def test_refuses_if_lock_is_symlink(self):
        (self.lock / "owner").unlink()
        self.lock.rmdir()
        real_target = self.root / "some_target"
        real_target.mkdir()
        try:
            self.lock.symlink_to(real_target)
        except OSError:
            self.skipTest("symlinks not supported")
        host_runner = FakeHostRunner(self.req, self.root)
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_refuses_if_lock_contains_extra_files(self):
        (self.lock / "extra_file.txt").write_text("unauthorized")
        host_runner = FakeHostRunner(self.req, self.root)
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_refuses_if_owner_is_not_32_hex(self):
        (self.lock / "owner").write_text("invalid-token\n")
        host_runner = FakeHostRunner(self.req, self.root)
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_refuses_if_owner_has_uppercase(self):
        (self.lock / "owner").write_text(OWNER_TOKEN.upper() + "\n")
        host_runner = FakeHostRunner(self.req, self.root)
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_refuses_if_supervisor_is_active(self):
        host_runner = FakeHostRunner(self.req, self.root)
        host_runner.command_overrides["systemctl show"] = "ActiveState=active\nSubState=running\nMainPID=123\nControlPID=0\n"
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_refuses_if_helper_container_exists(self):
        host_runner = FakeHostRunner(self.req, self.root)
        host_runner.command_overrides["docker ps -aq"] = "container_id_123\n"
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_refuses_if_active_db_migration(self):
        host_runner = FakeHostRunner(self.req, self.root)
        host_runner.command_overrides["psql"] = '{"owned":1,"ddl":0}'
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_refuses_if_active_deploy_process(self):
        host_runner = FakeHostRunner(self.req, self.root)
        host_runner.command_overrides["ps -eo"] = "1 /bin/init\n234 pwsh deploy.ps1\n"
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_refuses_if_live_image_differs_from_previous(self):
        host_runner = FakeHostRunner(self.req, self.root)
        host_runner.command_overrides["docker inspect codesamplex-server-1"] = json.dumps([{
            "Id": "container-new",
            "Image": "sha256:" + "f" * 64,
            "Config": {"Env": ["CSX_VERSION=" + PREV_SHA]},
            "State": {"Running": True, "OOMKilled": False, "StartedAt": "2026-09-14T09:00:00Z"},
            "RestartCount": 0
        }])
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_refuses_if_live_revision_differs_from_previous(self):
        host_runner = FakeHostRunner(self.req, self.root)
        host_runner.command_overrides["docker inspect codesamplex-server-1"] = json.dumps([{
            "Id": "container-new",
            "Image": PREV_IMAGE,
            "Config": {"Env": ["CSX_VERSION=" + TARGET_SHA]},
            "State": {"Running": True, "OOMKilled": False, "StartedAt": "2026-09-14T09:00:00Z"},
            "RestartCount": 0
        }])
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_refuses_if_active_docker_load_or_compose_mutation(self):
        host_runner = FakeHostRunner(self.req, self.root)
        host_runner.command_overrides["ps -eo"] = "1 /bin/init\n234 docker load -i image.tar\n"
        with self.assertRaises(host.Refusal):
            host_runner.run()

    def test_canonical_directory_platform_contract(self):
        # Real directories pass
        self.assertTrue(host.is_canonical_directory(self.root))
        self.assertTrue(host.is_canonical_directory(self.lock))

        # Files fail
        self.assertFalse(host.is_canonical_directory(self.lock / "owner"))

        # Non-existent paths fail
        self.assertFalse(host.is_canonical_directory(self.root / "nonexistent"))

        # Traversal with .. fails
        self.assertFalse(host.is_canonical_directory(self.lock / ".." / ".deploy-lock"))

        # Platform-specific canonical path variations on Windows
        if os.name == "nt":
            alt_case = Path(str(self.root).lower() if str(self.root) != str(self.root).lower() else str(self.root).upper())
            self.assertTrue(host.is_canonical_directory(alt_case))

    def test_verify_mode_with_case_or_short_path(self):
        if os.name != "nt":
            return
        alt_case = Path(str(self.root).lower() if str(self.root) != str(self.root).lower() else str(self.root).upper())
        host_runner = FakeHostRunner(self.req, alt_case)
        res = host_runner.run()
        self.assertEqual(res["lockState"], "owned")


if __name__ == "__main__":
    unittest.main()
