#!/usr/bin/env python3
"""Deterministic provenance and observer tamper regressions; no network or host."""
import copy
import importlib.util
import io
import json
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock
import warnings
import zipfile

spec = importlib.util.spec_from_file_location(
    "provenance", Path(__file__).with_name("reconciliation-provenance.py"))
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)
REPO = "r2cuerdame/CodeSampleX"
RUN_ID = "34608440406"
OWNER = "a" * 32
SHA = "b" * 40
TARGET = "c" * 40
PREVIOUS = "d" * 40


def encoded(value):
    return (json.dumps(value, separators=(",", ":")) + "\n").encode()


def archive_files(files):
    buffer = io.BytesIO()
    with warnings.catch_warnings():
        warnings.simplefilter("ignore", UserWarning)
        with zipfile.ZipFile(buffer, "w", zipfile.ZIP_DEFLATED) as archive:
            for name, raw in files:
                archive.writestr(name, raw)
    return buffer.getvalue()


def fixture():
    run = {"id": int(RUN_ID), "path": ".github/workflows/production-deploy.yml",
           "event": "workflow_dispatch", "head_branch": "main", "head_sha": SHA,
           "status": "completed", "conclusion": "failure", "run_number": 76, "run_attempt": 1,
           "repository": {"id": 123, "full_name": REPO},
           "head_repository": {"id": 123, "full_name": REPO}}
    jobs = [{"name": "Production eligibility", "run_id": run["id"], "head_sha": SHA,
             "status": "completed", "conclusion": "success"}]
    host = {"schemaVersion": 1, "owner": OWNER, "unit": "csx-migration-" + OWNER + ".service",
            "operationalSha": SHA, "targetSha": TARGET, "servedRevision": TARGET,
            "imageDigest": "sha256:" + "e" * 64, "migrationTimeoutSeconds": 1200,
            "phase": "committed", "conclusion": "success", "acceptanceAuthority": "host",
            "controllerSmoke": "host-verified", "health": "ok", "proxyHealth": "ok",
            "smoke": "pass", "representativeSmoke": "pass", "cleanup": "pass",
            "rollback": "not-started", "migrationVerification": "pass",
            "releaseTag": "v0.1.158", "serverStartedAt": "2026-09-11T14:15:58.802547Z",
            "migrationLedger": {"version": "0037_slow_query_indexes.sql", "count": 38},
            "indexes": [{"name": name, "valid": True, "ready": True,
                         "definition": p.INDEXES[name]} for name in sorted(p.INDEXES)],
            "completedAt": "2026-09-11T14:16:51.830430+00:00"}
    top = {"schemaVersion": 2, "workflowRunId": RUN_ID, "operationalSha": SHA,
           "targetSha": TARGET, "previousProductionSha": PREVIOUS,
           "previousImageDigest": "sha256:" + "f" * 64, "conclusion": "failure",
           "failureClass": "controller-unresolved", "rollback": "unknown-host-outcome",
           "migrationBudgetSeconds": 1200, "trackingIssue": "347",
           "offlineMigration": copy.deepcopy(host)}
    # Real PowerShell altered only ancillary timestamps in the nested document.
    top["offlineMigration"]["completedAt"] = "2026-09-11T14:16:51.83043+00:00"
    artifact = {"id": 456, "name": "production-evidence-" + RUN_ID, "expired": False,
                "size_in_bytes": 4000, "workflow_run": {
                    "id": run["id"], "repository_id": 123, "head_repository_id": 123,
                    "head_sha": SHA, "head_branch": "main"}}
    return run, jobs, artifact, top, host


class ProvenanceTests(unittest.TestCase):
    def setUp(self):
        self.run, self.jobs, self.artifact, self.top, self.host = fixture()

    def valid(self, archive=None):
        raw = {p.TOP_NAME: encoded(self.top), p.HOST_NAME: encoded(self.host)}
        if archive is None:
            archive = archive_files(raw.items())
        self.artifact["digest"] = "sha256:" + p.sha256(archive)
        artifact = p.select_artifact([self.artifact], self.run)
        return p.validate_original(self.run, self.jobs, artifact,
                                   p.read_artifact(archive, artifact), REPO, RUN_ID, OWNER,
                                   p.sha256(archive))

    def test_exact_raw_timestamp_hash_and_previous_are_preserved(self):
        expected, provenance = self.valid()
        self.assertEqual(expected["serverStartedAt"], self.host["serverStartedAt"])
        self.assertEqual(expected["previousSha"], PREVIOUS)
        self.assertEqual(expected["evidenceSha256"], p.sha256(encoded(self.host)))
        self.assertEqual(provenance["originalDeploymentRunNumber"], 76)
        self.assertEqual(provenance["originalEvidenceSha256"], p.sha256(encoded(self.top)))

    def test_run_provenance_mutations(self):
        cases = {"id": 1, "path": ".github/workflows/ci.yml", "event": "pull_request",
                 "head_branch": "feature", "head_sha": TARGET, "status": "in_progress",
                 "conclusion": "success", "run_attempt": True, "run_number": 0,
                 "repository": {"id": 999, "full_name": REPO},
                 "head_repository": {"id": 999, "full_name": "foreign/fork"}}
        for field, value in cases.items():
            with self.subTest(field=field):
                self.setUp()
                self.run[field] = value
                with self.assertRaises(p.ProvenanceError):
                    self.valid()

    def test_job_must_be_unique_and_successful_for_original_head(self):
        for field, value in {"run_id": 1, "head_sha": TARGET, "status": "queued",
                             "conclusion": "failure", "name": "Wrong"}.items():
            with self.subTest(field=field):
                self.setUp()
                self.jobs[0][field] = value
                with self.assertRaises(p.ProvenanceError):
                    self.valid()
        self.setUp()
        self.jobs *= 2
        with self.assertRaises(p.ProvenanceError):
            self.valid()

    def test_artifact_metadata_and_duplicates(self):
        for field, value in {"id": True, "expired": True, "size_in_bytes": p.MAX_ARCHIVE_BYTES + 1,
                             "workflow_run": {}}.items():
            with self.subTest(field=field):
                self.setUp()
                self.artifact[field] = value
                with self.assertRaises(p.ProvenanceError):
                    self.valid()
        self.setUp()
        with self.assertRaises(p.ProvenanceError):
            p.select_artifact([self.artifact, self.artifact], self.run)
        with self.assertRaises(p.ProvenanceError):
            p.select_artifact([], self.run)

    def test_original_failure_cannot_be_other_failure_or_success(self):
        for field, value in {"schemaVersion": 3, "workflowRunId": "1",
                             "operationalSha": TARGET, "conclusion": "success",
                             "failureClass": "pre-activation", "rollback": "succeeded",
                             "previousProductionSha": TARGET, "targetSha": "short",
                             "previousImageDigest": self.host["imageDigest"],
                             "migrationBudgetSeconds": 60, "trackingIssue": "foreign/347"}.items():
            with self.subTest(field=field):
                self.setUp()
                self.top[field] = value
                with self.assertRaises(p.ProvenanceError):
                    self.valid()

    def test_every_host_acceptance_field_is_required_and_bound(self):
        fields = ("schemaVersion", "owner", "unit", "operationalSha", "targetSha",
                  "servedRevision", "phase", "conclusion", "acceptanceAuthority",
                  "controllerSmoke", "migrationVerification", "health", "proxyHealth",
                  "smoke", "representativeSmoke", "cleanup", "rollback", "imageDigest",
                  "releaseTag", "migrationTimeoutSeconds", "serverStartedAt")
        for field in fields:
            with self.subTest(field=field):
                self.setUp()
                del self.host[field]
                with self.assertRaises(p.ProvenanceError):
                    self.valid()
        for stamp in (True, "2026-09-11T14:15:58.802547000Z",
                      "2026-09-11T14:15:58.802547+00:00",
                      "2026-09-11T14:15:58.802547+09:00", "2026-99-11T14:15:58Z",
                      "2026-09-11T14:15:58.8025471234Z"):
            with self.subTest(stamp=stamp):
                self.setUp()
                self.host["serverStartedAt"] = stamp
                with self.assertRaises(p.ProvenanceError):
                    self.valid()

    def test_nested_evidence_cannot_disagree(self):
        for field in ("owner", "imageDigest", "serverStartedAt", "releaseTag",
                      "migrationLedger", "health", "proxyHealth", "cleanup", "indexes"):
            with self.subTest(field=field):
                self.setUp()
                self.top["offlineMigration"][field] = "wrong"
                with self.assertRaises(p.ProvenanceError):
                    self.valid()

    def test_wrong_ledger_index_or_rollback_evidence(self):
        mutations = (
            lambda h: h["migrationLedger"].update(count=37),
            lambda h: h["migrationLedger"].update(count=38.0),
            lambda h: h["migrationLedger"].update(version="0038_unreviewed.sql", count=39),
            lambda h: h["indexes"][0].update(valid=False),
            lambda h: h["indexes"][0].update(ready=1),
            lambda h: h["indexes"][0].update(definition="CREATE INDEX wrong"),
            lambda h: h["indexes"].pop(),
            lambda h: h.update(rollbackFailures=[]),
        )
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                self.setUp()
                mutation(self.host)
                self.top["offlineMigration"] = copy.deepcopy(self.host)
                with self.assertRaises(p.ProvenanceError):
                    self.valid()

    def test_archive_hash_paths_duplicates_size_and_symlink(self):
        files = [(p.TOP_NAME, encoded(self.top)), (p.HOST_NAME, encoded(self.host))]
        for broken in (files[:1], files + [files[0]],
                       [("../" + name, raw) for name, raw in files],
                       [(p.TOP_NAME, b" " * (p.MAX_JSON_BYTES + 1)), files[1]]):
            with self.subTest(names=[str(item[0]) for item in broken]):
                with self.assertRaises(p.ProvenanceError):
                    self.valid(archive_files(broken))
        link = zipfile.ZipInfo(p.TOP_NAME)
        link.create_system = 3
        link.external_attr = (stat.S_IFLNK | 0o777) << 16
        with self.assertRaises(p.ProvenanceError):
            self.valid(archive_files([(link, b"target"), files[1]]))
        archive = archive_files(files)
        with self.assertRaises(p.ProvenanceError):
            p.read_artifact(archive, {"digest": "sha256:" + "0" * 64})
        # Extra traversal entries are never read or extracted.
        self.valid(archive_files(files + [("../../must-not-exist", b"ignored")]))

    def test_json_duplicate_keys_nonfinite_and_invalid_types(self):
        for raw in (b'{"x":1,"x":2}', b'{"x":NaN}', b'{"x":Infinity}', b'[]', b'null', b'\xff'):
            with self.subTest(raw=raw), self.assertRaises(p.ProvenanceError):
                p.parse_json(raw)

    def test_api_timeout_and_complete_listing_fail_closed(self):
        with mock.patch.object(p.subprocess, "run",
                               side_effect=subprocess.TimeoutExpired("gh", p.API_TIMEOUT)) as call:
            with self.assertRaisesRegex(p.ProvenanceError, "github-api-failed"):
                p.gh_bytes("repos/example/repo")
            self.assertEqual(call.call_args.kwargs["timeout"], 60)
            self.assertEqual(call.call_args.kwargs["stdin"], subprocess.DEVNULL)
        for response in ({"total_count": 101, "jobs": []},
                         {"total_count": True, "jobs": [{}]},
                         {"total_count": 1, "jobs": []}):
            with mock.patch.object(p, "gh_json", return_value=response):
                with self.assertRaises(p.ProvenanceError):
                    p.gh_collection("endpoint", "jobs")

    def test_outputs_preserve_raw_and_refuse_overwrite(self):
        expected, provenance = self.valid()
        raw = {p.TOP_NAME: encoded(self.top), p.HOST_NAME: encoded(self.host)}
        archive = archive_files(raw.items())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            p.write_outputs(path, expected, provenance, self.run, raw, archive)
            self.assertEqual((path / p.HOST_NAME).read_bytes(), raw[p.HOST_NAME])
            with self.assertRaisesRegex(p.ProvenanceError, "output-already-exists"):
                p.write_outputs(path, expected, provenance, self.run, raw, archive)

    def reconciled(self):
        expected, provenance = self.valid()
        run = dict(self.run, id=999, run_number=1, head_sha="1" * 40, conclusion="success",
                   path=".github/workflows/production-reconcile.yml")
        identity = {"imageDigest": expected["imageDigest"], "configuredRevision": TARGET,
                    "serverStartedAt": expected["serverStartedAt"], "releaseTag": "v0.1.158",
                    "restartCount": 0}
        supervisor = {"Id": "csx-migration-" + OWNER + ".service", "LoadState": "not-found",
                      "ActiveState": "inactive", "SubState": "dead", "MainPID": "0",
                      "ControlPID": "0", "ControlGroup": ""}
        host = dict(expected, conclusion="success", lockDisposition="retained",
                    retainedEvidenceSha256=expected["evidenceSha256"], retainedConfigSha256="2" * 64,
                    acceptanceAuthority="host-reconciliation", migrationLedger=self.host["migrationLedger"],
                    servedRevision=TARGET, health="ok", proxyHealth="ok", smoke="pass",
                    representativeSmoke="pass", cleanup="pass", checks={key: "pass" for key in p.CHECKS},
                    indexes=copy.deepcopy(self.host["indexes"]),
                    identityBefore=copy.deepcopy(identity), identityAfter=copy.deepcopy(identity),
                    supervisorBefore=copy.deepcopy(supervisor), supervisorAfter=copy.deepcopy(supervisor))
        evidence = {"schemaVersion": 3, "workflowRunId": "999", "operationalSha": run["head_sha"],
                    "conclusion": "success", "targetSha": TARGET, "deployedSha": TARGET,
                    "servedRevision": TARGET, "previousProductionSha": PREVIOUS,
                    "imageDigest": expected["imageDigest"], "serverStartedAt": expected["serverStartedAt"],
                    "migrationVersion": expected["expectedMigration"], "health": "ok", "smoke": "pass",
                    "rollback": "not-needed", "failureClass": "none", "acceptanceAuthority": "host-reconciled",
                    "trackingIssue": "347", "hostReconciliation": host,
                    "publicBefore": {"healthz": "ok", "version": {"revision": TARGET, "version": "v0.1.158"}},
                    "publicAfter": {"healthz": "ok", "version": {"revision": TARGET, "version": "v0.1.158"}},
                    "reconciliation": dict(provenance, kind="committed-host", lockDisposition="retained")}
        return evidence, expected, provenance, run

    def test_reconciliation_all_bindings_and_checks(self):
        evidence, expected, provenance, run = self.reconciled()
        p.validate_reconciled(evidence, expected, provenance, run)
        for section in (None, "reconciliation", "hostReconciliation"):
            original = evidence if section is None else evidence[section]
            # Every required field deletion must refuse observation.
            keys = set(original)
            if section == "reconciliation":
                keys -= {"schemaVersion", "repository", "originalRunAttempt", "trackingIssue"}
            for key in keys:
                with self.subTest(section=section, key=key):
                    broken = copy.deepcopy(evidence)
                    del (broken if section is None else broken[section])[key]
                    with self.assertRaises(p.ProvenanceError):
                        p.validate_reconciled(broken, expected, provenance, run)
        for key in p.CHECKS:
            with self.subTest(check=key):
                broken = copy.deepcopy(evidence)
                broken["hostReconciliation"]["checks"][key] = "failure"
                with self.assertRaises(p.ProvenanceError):
                    p.validate_reconciled(broken, expected, provenance, run)
        broken = copy.deepcopy(evidence)
        broken["hostReconciliation"]["identityAfter"]["restartCount"] = 1
        with self.assertRaises(p.ProvenanceError):
            p.validate_reconciled(broken, expected, provenance, run)

    def test_shared_host_and_public_acceptance_validate_exact_reports(self):
        evidence, expected, provenance, run = self.reconciled()
        host = evidence["hostReconciliation"]
        self.assertIs(p.validate_host_report(host, expected), host)
        self.assertIs(p.validate_public_probe(evidence["publicBefore"], expected),
                      evidence["publicBefore"])
        mutations = (
            None, {}, {"healthz": "ok"}, {"version": {"revision": TARGET, "version": "v0.1.158"}},
            {"healthz": "503", "version": {"revision": TARGET, "version": "v0.1.158"}},
            {"healthz": "ok", "version": {"revision": PREVIOUS, "version": "v0.1.158"}},
            {"healthz": "ok", "version": {"revision": TARGET, "version": "v0.1.157"}},
            {"healthz": "ok", "version": {"revision": TARGET}},
            {"healthz": "ok", "version": {"revision": TARGET, "version": "v0.1.158", "extra": True}},
            {"healthz": "ok", "version": {"revision": TARGET, "version": "v0.1.158"}, "extra": True},
        )
        for section in ("publicBefore", "publicAfter"):
            for value in mutations:
                with self.subTest(section=section, value=value):
                    broken = copy.deepcopy(evidence)
                    broken[section] = value
                    with self.assertRaises(p.ProvenanceError):
                        p.validate_public_probe(value, expected)
                    with self.assertRaises(p.ProvenanceError):
                        p.validate_reconciled(broken, expected, provenance, run)
        broken_host = copy.deepcopy(host)
        del broken_host["identityAfter"]
        with self.assertRaises(p.ProvenanceError):
            p.validate_host_report(broken_host, expected)
        broken = copy.deepcopy(evidence)
        broken["reconciliation"]["originalDeploymentRunNumber"] = 76.0
        with self.assertRaises(p.ProvenanceError):
            p.validate_reconciled(broken, expected, provenance, run)


if __name__ == "__main__":
    unittest.main()
