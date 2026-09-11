import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile


ROOT = Path(__file__).resolve().parent


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


p = module("provenance_test_subject", "reconciliation-provenance.py")
c = module("controller_test_subject", "reconcile-production.py")
REPO = "owner/repository"


def raw(value):
    return (json.dumps(value) + "\n").encode()


def run(run_id=100, path=p.DEPLOY, conclusion="failure", attempt=1):
    return {"id": run_id, "path": path, "event": "workflow_dispatch", "status": "completed",
            "conclusion": conclusion, "head_branch": "main", "head_sha": "a" * 40,
            "run_number": 45 if path == p.DEPLOY else 2, "run_attempt": attempt,
            "repository": {"full_name": REPO}, "head_repository": {"full_name": REPO}}


def job(source_run, name, conclusion):
    return {"name": name, "run_id": source_run["id"], "run_attempt": source_run["run_attempt"],
            "status": "completed", "conclusion": conclusion,
            "started_at": "2026-09-11T14:00:00Z", "completed_at": "2026-09-11T14:20:00Z",
            "steps": [{"name": "Deploy and verify", "conclusion": conclusion}]}


def source_values():
    source_run = run()
    host = {"schemaVersion": 1, "owner": "b" * 32, "unit": "csx-migration-" + "b" * 32 + ".service",
            "phase": "committed", "conclusion": "success", "acceptanceAuthority": "host", "controllerSmoke": "host-verified",
            "operationalSha": "a" * 40, "targetSha": "c" * 40, "servedRevision": "c" * 40,
            "imageDigest": "sha256:" + "d" * 64, "serverStartedAt": "2026-09-11T14:15:58.802547123Z",
            "completedAt": "2026-09-11T14:16:51.830430+00:00", "activationStartedAt": "2026-09-11T14:15:57.1Z",
            "releaseTag": "v0.1.158", "migrationLedger": {"version": "0037_slow_query_indexes.sql", "count": 38},
            "migrationTimeoutSeconds": 1200,
            "health": "ok", "proxyHealth": "ok", "smoke": "pass", "representativeSmoke": "pass", "cleanup": "pass",
            "migrationVerification": "pass", "rollback": "not-started"}
    top = {"schemaVersion": 2, "conclusion": "failure", "failureClass": "controller-unresolved", "workflowRunId": "100",
           "rollback": "unknown-host-outcome",
           "operationalSha": "a" * 40, "targetSha": "c" * 40, "previousProductionSha": "e" * 40,
           "previousImageDigest": "sha256:" + "f" * 64, "trackingIssue": "347",
           "migrationBudgetSeconds": 1200, "offlineMigration": copy.deepcopy(host)}
    # Real artifact: the old controller normalized this nonidentity field.
    top["offlineMigration"]["completedAt"] = "2026-09-11T14:16:51.83043+00:00"
    jobs = [job(source_run, "Production eligibility", "success"), job(source_run, "Roll out production", "failure")]
    artifact = {"id": 700, "name": "production-evidence-100", "expired": False,
                "created_at": "2026-09-11T14:17:16Z", "digest": "sha256:" + "1" * 64,
                "workflow_run": {"id": 100, "head_sha": "a" * 40, "head_branch": "main"}}
    return source_run, jobs, artifact, top, host


class API:
    repository = REPO

    def __init__(self):
        self.values = {}
        self.lists = {}

    def api(self, path, binary=False):
        return copy.deepcopy(self.values[path])

    def pages(self, path, key):
        return copy.deepcopy(self.lists[(path, key)])

    def artifact(self, metadata, files):
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, "w", zipfile.ZIP_DEFLATED) as archive:
            for filename, content in files.items():
                archive.writestr(filename, content)
        metadata["digest"] = "sha256:" + p.digest(stream.getvalue())
        self.values["actions/artifacts/" + str(metadata["id"])] = metadata
        self.values["actions/artifacts/" + str(metadata["id"]) + "/zip"] = stream.getvalue()


def authenticated_source():
    api = API()
    source_run, jobs, artifact, top, host = source_values()
    api.values["actions/runs/100"] = source_run
    api.values["actions/runs/100/attempts/1"] = source_run
    api.lists[("actions/runs/100/attempts/1/jobs", "jobs")] = jobs
    api.artifact(artifact, {p.TOP: raw(top), p.HOST: raw(host)})
    api.lists[("actions/runs/100/artifacts", "artifacts")] = [artifact]
    return api, p.fetch_source(api, "100", 1, 700)


def released_values():
    api, bundle = authenticated_source()
    request = c.make_request(bundle, "a" * 40, "200", 1)
    result = {"schemaVersion": 1, "verifiedAt": "2026-09-11T14:18:00Z", "binding": c.expected_binding(request),
              "lockState": "owned", "health": "ok", "smoke": "pass", "cleanup": "pass", "receipt": None}
    prepared = {"schemaVersion": 1, "request": copy.deepcopy(request), "verification": copy.deepcopy(result)}
    prepared_run = run(200, p.RECONCILE, "failure")
    api.values["actions/runs/200/attempts/1"] = prepared_run
    artifact = {"id": 800, "name": "production-reconciliation-prepared-200-1", "expired": False,
                "workflow_run": {"id": 200, "head_sha": "a" * 40, "head_branch": "main"}}
    api.artifact(artifact, {p.PREPARED: raw(prepared)})
    proof = {"id": 800, "digest": artifact["digest"], "runId": "200", "runAttempt": 1}
    receipt = {"schemaVersion": 1, "binding": copy.deepcopy(result["binding"]), "preparedArtifact": proof,
               "reconciliationRunId": "200", "reconciliationRunAttempt": 1, "operationalSha": "a" * 40,
               "verifiedAt": result["verifiedAt"]}
    result["lockState"], result["receipt"] = "archived", receipt
    # A new successful run can publish after the preceding run lost its output.
    request["reconciliationRunId"] = "300"
    request["mode"], request["preparedArtifact"] = "release", proof
    current = run(300, p.RECONCILE, "success")
    api.lists[("actions/runs/300/attempts/1/jobs", "jobs")] = [job(current, "Reconcile committed production owner", "success")]
    final = c.evidence(bundle, request, result)
    return api, bundle, request, result, current, final


class ReconciliationProvenanceTests(unittest.TestCase):
    def test_exact_original_identity_and_raw_hash_survive_old_timestamp_normalization(self):
        api, bundle = authenticated_source()
        self.assertEqual(bundle["hostEvidence"]["serverStartedAt"], "2026-09-11T14:15:58.802547123Z")
        self.assertEqual(bundle["hostEvidenceSha256"], p.digest(raw(source_values()[4])))
        self.assertEqual(bundle["sourceRun"]["run_number"], 45)

    def test_wrong_workflow_repository_branch_event_and_outcome_refuse(self):
        for key, value in [("path", p.RECONCILE), ("head_branch", "test"), ("event", "pull_request"),
                           ("conclusion", "success"), ("status", "in_progress"), ("run_attempt", False),
                           ("head_repository", {"full_name": "attacker/fork"})]:
            with self.subTest(key=key):
                original, jobs, artifact, top, host = source_values()
                original[key] = value
                with self.assertRaises(ValueError):
                    p.validate_source(original, jobs, artifact, raw(top), raw(host), REPO)

    def test_source_job_provenance_and_original_time_window_refuse_stale_attempt(self):
        changes = [lambda r, j, a, t, h: j[1].update(run_attempt=2),
                   lambda r, j, a, t, h: j[0].update(conclusion="failure"),
                   lambda r, j, a, t, h: j.append(copy.deepcopy(j[1])),
                   lambda r, j, a, t, h: a.update(created_at="2026-09-11T13:17:16Z"),
                   lambda r, j, a, t, h: h.update(activationStartedAt="2026-09-11T13:15:57Z"),
                   lambda r, j, a, t, h: h.update(completedAt="2026-09-11T15:16:51Z"),
                   lambda r, j, a, t, h: t.update(rollback="pass"),
                   lambda r, j, a, t, h: t.update(migrationBudgetSeconds=60),
                   lambda r, j, a, t, h: h.update(migrationTimeoutSeconds=True),
                   lambda r, j, a, t, h: h.update(migrationTimeoutSeconds=1801),
                   lambda r, j, a, t, h: j[1]["steps"].append({"name": "cleanup", "conclusion": "failure"})]
        for change in changes:
            with self.subTest(change=change):
                values = list(source_values())
                change(*values)
                with self.assertRaises(ValueError):
                    p.validate_source(*values[:3], raw(values[3]), raw(values[4]), REPO)

    def test_every_critical_host_identity_and_acceptance_mismatch_refuses(self):
        _, _, _, _, baseline = source_values()
        keys = ["owner", "unit", "targetSha", "imageDigest", "serverStartedAt", "operationalSha", "migrationLedger",
                "releaseTag", "cleanup", "health", "proxyHealth", "smoke", "representativeSmoke", "phase",
                "conclusion", "acceptanceAuthority", "controllerSmoke", "servedRevision", "migrationVerification", "rollback"]
        for key in keys:
            with self.subTest(key=key):
                original, jobs, artifact, top, host = source_values()
                host[key] = "different"
                with self.assertRaises(ValueError):
                    p.validate_source(original, jobs, artifact, raw(top), raw(host), REPO)

    def test_artifact_digest_duplicate_and_traversal_members_refuse(self):
        api, bundle = authenticated_source()
        artifact = copy.deepcopy(bundle["sourceArtifact"])
        artifact["digest"] = "sha256:" + "0" * 64
        with self.assertRaises(ValueError):
            p.artifact_files(api, artifact, [p.TOP, p.HOST], bundle["sourceRun"])
        api.artifact(artifact, {"../" + p.TOP: b"{}", p.HOST: b"{}"})
        with self.assertRaises(ValueError):
            p.artifact_files(api, artifact, [p.TOP, p.HOST], bundle["sourceRun"])
        api.lists[("actions/runs/100/artifacts", "artifacts")].append(copy.deepcopy(artifact))
        with self.assertRaises(ValueError):
            p.named_artifact(api, bundle["sourceRun"], artifact["name"])

    def test_duplicate_keys_nonfinite_and_nanosecond_loss_refuse(self):
        for data in [b'{"a":1,"a":2}', b'{"a":NaN}']:
            with self.assertRaises(ValueError):
                p.unique_json(data)
        self.assertEqual(p.timestamp("2026-09-11T14:15:58.802547124Z") -
                         p.timestamp("2026-09-11T14:15:58.802547123Z"), 1)

    def test_missing_lock_requires_exact_prepared_archive_and_preserves_deploy_order(self):
        api, bundle, request, result, current, final = released_values()
        c.validate_result(result, request, released=True)
        self.assertEqual(c.authenticate_receipt(api, result, request), request["preparedArtifact"])
        self.assertEqual(p.validate_observation(api, current, final), 45)
        self.assertEqual(final["workflowRunId"], "300")
        self.assertEqual(final["previousProductionSha"], "e" * 40)
        self.assertEqual(final["reconciliation"]["sourceRunId"], "100")

    def test_source_attempt_and_artifact_are_explicit_and_not_latest(self):
        api, bundle = authenticated_source()
        latest = run(attempt=2)
        api.values["actions/runs/100"] = latest
        api.values["actions/runs/100/attempts/2"] = latest
        later_jobs = [job(latest, "Production eligibility", "success"),
                      job(latest, "Roll out production", "failure")]
        for item in later_jobs:
            item.update(started_at="2026-09-11T15:00:00Z", completed_at="2026-09-11T15:20:00Z")
        api.lists[("actions/runs/100/attempts/2/jobs", "jobs")] = later_jobs
        later_artifact = copy.deepcopy(bundle["sourceArtifact"])
        later_artifact.update(id=701, created_at="2026-09-11T15:17:16Z")
        api.artifact(later_artifact, {p.TOP: raw(bundle["sourceEvidence"]), p.HOST: raw(bundle["hostEvidence"])})
        # Same artifact name now exists for another attempt. The exact original
        # ID remains authoritative; listing order and latest run do not matter.
        api.lists[("actions/runs/100/artifacts", "artifacts")].insert(0, later_artifact)
        with patch.object(api, "api", wraps=api.api) as requests:
            self.assertEqual(p.fetch_source(api, "100", 1, 700), bundle)
        self.assertNotIn("actions/runs/100", [call.args[0] for call in requests.call_args_list])
        for attempt, artifact_id in ((1, 701), (2, 700), (True, 700), (1, True), (0, 700), (1, None)):
            with self.subTest(attempt=attempt, artifact_id=artifact_id), self.assertRaises(ValueError):
                p.fetch_source(api, "100", attempt, artifact_id)
        for field, value in (("id", 999), ("name", "foreign-artifact"), ("expired", True)):
            with self.subTest(field=field):
                changed = copy.deepcopy(bundle["sourceArtifact"])
                changed[field] = value
                api.values["actions/artifacts/700"] = changed
                with self.assertRaises(ValueError):
                    p.fetch_source(api, "100", 1, 700)
        api.values["actions/artifacts/700"] = bundle["sourceArtifact"]
        api.values["actions/runs/100/attempts/1"] = latest
        with self.assertRaisesRegex(ValueError, "attempt mismatch"):
            p.fetch_source(api, "100", 1, 700)

    def test_source_artifact_ambiguity_expiry_and_interval_refuse_before_download(self):
        for mutation in ("missing", "ambiguous", "expired", "outside"):
            with self.subTest(mutation=mutation):
                api, bundle = authenticated_source()
                artifacts = api.lists[("actions/runs/100/artifacts", "artifacts")]
                if mutation == "missing":
                    artifacts.clear()
                elif mutation == "ambiguous":
                    artifacts.append(dict(artifacts[0], id=702))
                elif mutation == "expired":
                    artifacts[0]["expired"] = True
                else:
                    artifacts[0]["created_at"] = "2026-09-11T15:17:16Z"
                with patch.object(api, "api", wraps=api.api) as requests:
                    with self.assertRaisesRegex(ValueError, "exactly one unexpired source artifact"):
                        p.fetch_source(api, "100", 1, 700)
                self.assertFalse(any(call.args[0].endswith("/zip") for call in requests.call_args_list))
        api, bundle = authenticated_source()
        api.lists[("actions/runs/100/attempts/1/jobs", "jobs")][0]["conclusion"] = "failure"
        with patch.object(api, "api", wraps=api.api) as requests:
            with self.assertRaises(ValueError):
                p.fetch_source(api, "100", 1, 700)
        self.assertFalse(any(call.args[0].startswith("actions/artifacts/") for call in requests.call_args_list))

    def test_source_cli_requires_both_explicit_attempt_and_artifact_before_api(self):
        baseline = ["reconciliation-provenance.py", "fetch-source", "--repository", REPO,
                    "--run-id", "100", "--output", "must-not-write.json"]
        for flags in ([], ["--run-attempt", "1"], ["--artifact-id", "700"]):
            with self.subTest(flags=flags), patch("sys.argv", baseline + flags), \
                    patch("sys.stderr", new_callable=io.StringIO) as error, patch.object(p, "GitHub") as api:
                with self.assertRaises(SystemExit) as failed:
                    p.main()
                self.assertEqual(failed.exception.code, 2)
                self.assertIn("--run-attempt and --artifact-id are required", error.getvalue())
                api.assert_not_called()

    def test_later_source_attempt_cannot_invalidate_completed_observation_or_archived_retry(self):
        api, bundle, request, result, current, final = released_values()
        # The original workflow has since been rerun and failed on the retained
        # or now-archived lock. All earlier immutable proof remains unchanged.
        api.values["actions/runs/100"] = run(attempt=2)
        self.assertEqual(p.validate_observation(api, current, final), 45)
        self.assertEqual(c.authenticate_receipt(api, result, request), request["preparedArtifact"])
        new_request = c.make_request(bundle, "a" * 40, "300", 1)
        prepared = {"schemaVersion": 1, "request": new_request, "verification": result}
        api.values["actions/runs/300/attempts/1"] = current
        metadata = {"id": 801, "name": "production-reconciliation-prepared-300-1", "expired": False,
                    "workflow_run": {"id": 300, "head_sha": "a" * 40, "head_branch": "main"}}
        api.artifact(metadata, {p.PREPARED: raw(prepared)})
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            bundle_path, prepared_path, output = (directory / name for name in ("source.json", "prepared.json", "final.json"))
            bundle_path.write_bytes(raw(bundle))
            prepared_path.write_bytes(raw(prepared))
            argv = ["reconcile-production.py", "release", "--bundle", str(bundle_path),
                    "--prepared", str(prepared_path), "--prepared-artifact-id", "801",
                    "--prepared-artifact-digest", metadata["digest"], "--output", str(output)]
            with patch.dict(os.environ, {"GITHUB_REPOSITORY": REPO, "GITHUB_SHA": "a" * 40,
                                         "GITHUB_RUN_ID": "300", "GITHUB_RUN_ATTEMPT": "1",
                                         "PRODUCTION_HOST": "production.example", "PRODUCTION_USER": "ubuntu",
                                         "PRODUCTION_KEY_PATH": "key", "PRODUCTION_KNOWN_HOSTS_PATH": "known"}), \
                    patch("sys.argv", argv), patch.object(c.provenance, "GitHub", return_value=api), \
                    patch.object(c, "public_health"), patch.object(c, "remote", return_value=result) as remote:
                c.main()
            published = p.unique_json(output.read_bytes())
            sent = remote.call_args.args[0]
            self.assertEqual(sent["sourceRunAttempt"], 1)
            self.assertEqual(sent["sourceArtifactId"], 700)
            self.assertEqual(sent["preparedArtifact"], request["preparedArtifact"])
            self.assertEqual(published["reconciliation"]["verification"]["receipt"], result["receipt"])
            self.assertEqual(p.validate_observation(api, current, published), 45)

    def test_observer_rejects_changed_source_attempt_artifact_or_budget(self):
        for key, value in (("sourceRunAttempt", 2), ("sourceArtifactId", 701),
                           ("sourceRunAttempt", True), ("sourceArtifactId", True)):
            with self.subTest(key=key):
                api, bundle, request, result, current, final = released_values()
                final["reconciliation"][key] = value
                with self.assertRaises((ValueError, KeyError)):
                    p.validate_observation(api, current, final)
        api, bundle, request, result, current, final = released_values()
        result["binding"]["migrationTimeoutSeconds"] = 60
        with self.assertRaises(ValueError):
            p.validate_observation(api, current, final)

    def test_failed_original_or_reconciliation_never_becomes_observer_eligible(self):
        api, bundle, request, result, current, final = released_values()
        for rejected in [bundle["sourceRun"], dict(current, conclusion="failure"), dict(current, run_attempt=2)]:
            with self.assertRaises(ValueError):
                p.validate_observation(api, rejected, final)

    def test_receipt_forgery_foreign_identity_or_unpublished_preparation_refuses(self):
        changes = [lambda x: x.update(lockState="owned"), lambda x: x.update(receipt=None),
                   lambda x: x["binding"].update(serverStartedAt="2026-09-11T14:15:59Z"),
                   lambda x: x["receipt"].update(reconciliationRunId="999"),
                   lambda x: x["receipt"]["preparedArtifact"].update(digest="sha256:" + "0" * 64)]
        for change in changes:
            with self.subTest(change=change):
                api, bundle, request, result, current, final = released_values()
                change(result)
                with self.assertRaises((ValueError, KeyError)):
                    p.validate_observation(api, current, c.evidence(bundle, request, result))

    def test_public_health_failure_precedes_all_host_activity(self):
        api, bundle = authenticated_source()
        with tempfile.TemporaryDirectory() as directory:
            bundle_path = Path(directory) / "source.json"
            bundle_path.write_bytes(raw(bundle))
            argv = ["reconcile-production.py", "verify", "--bundle", str(bundle_path), "--output", str(Path(directory) / "result.json")]
            with patch.dict(os.environ, {"GITHUB_REPOSITORY": REPO, "GITHUB_SHA": "a" * 40,
                                         "GITHUB_RUN_ID": "200", "GITHUB_RUN_ATTEMPT": "1"}), \
                    patch("sys.argv", argv), patch.object(c.provenance, "fetch_source", return_value=bundle), \
                    patch.object(c, "public_health", side_effect=ValueError("unhealthy")), patch.object(c, "remote") as remote:
                with self.assertRaises(ValueError):
                    c.main()
                remote.assert_not_called()
                self.assertFalse((Path(directory) / "result.json").exists())

    def test_unpublished_preparation_cannot_release(self):
        api, bundle, request, result, current, final = released_values()
        with patch.object(api, "api", side_effect=ValueError("artifact missing")):
            with self.assertRaises(ValueError):
                c.authenticate_receipt(api, result, request)

    def test_observer_writes_only_authenticated_fixed_json_and_never_extracts(self):
        for hostile in (False, True):
            with self.subTest(hostile=hostile):
                api, bundle, request, result, current, final = released_values()
                api.values["actions/runs/300"] = current
                metadata = {"id": 900, "name": "production-evidence-300-1", "expired": False,
                            "workflow_run": {"id": 300, "head_sha": "a" * 40, "head_branch": "main"}}
                files = {p.TOP: raw(final)}
                if hostile:
                    files["../reconciliation-provenance.py"] = b"raise RuntimeError('untrusted archive code')"
                api.artifact(metadata, files)
                api.lists[("actions/runs/300/artifacts", "artifacts")] = [metadata]
                with tempfile.TemporaryDirectory() as directory:
                    output = Path(directory) / p.TOP
                    argv = ["reconciliation-provenance.py", "download-observation", "--repository", REPO,
                            "--run-id", "300", "--output", str(output)]
                    with patch("sys.argv", argv), patch.object(p, "GitHub", return_value=api), \
                            patch("builtins.print") as printed:
                        if hostile:
                            with self.assertRaises(ValueError):
                                p.main()
                            self.assertFalse(output.exists())
                        else:
                            p.main()
                            self.assertEqual(p.unique_json(output.read_bytes()), final)
                            printed.assert_called_once_with(45)
                    self.assertEqual(list(Path(directory).iterdir()), [] if hostile else [output])


if __name__ == "__main__":
    unittest.main()
