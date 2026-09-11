#!/usr/bin/env python3
"""Authenticate failed-deploy evidence before read-only owner reconciliation.

This helper only reads GitHub. It never contacts production or extracts archives.
"""
import argparse
import datetime
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import zipfile

TOP_NAME = "production-deploy-evidence.json"
HOST_NAME = TOP_NAME + ".migration.json"
MAX_ARCHIVE_BYTES = 8 * 1024 * 1024
MAX_JSON_BYTES = 2 * 1024 * 1024
API_TIMEOUT = 60
SHA = re.compile(r"[0-9a-f]{40}")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
HEX = re.compile(r"[0-9a-f]{64}")
OWNER = re.compile(r"[0-9a-f]{32}")
NUMBER = re.compile(r"[1-9][0-9]*")
REPO = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+")
STARTED = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|\+00:00)")
MIGRATION = re.compile(r"[0-9]{4}_[a-z0-9_]+\.sql")
RELEASE = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
# Read reviewed definitions from the operational checkout, never retained host code.
_spec = importlib.util.spec_from_file_location(
    "csx_provenance_migration", Path(__file__).with_name("offline-migration.py"))
_migration = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_migration)
INDEXES = _migration.REVIEWED_MIGRATIONS["0037_slow_query_indexes.sql"]["indexes"]
CHECKS = ("retained", "supervisor-before", "identity-before", "database", "health-smoke",
          "identity-after", "cleanup-after", "supervisor-after", "retained-stable")


class ProvenanceError(ValueError):
    pass


def require(condition, code):
    if not condition:
        raise ProvenanceError(code)


def formatted(value, pattern, code):
    require(type(value) is str and pattern.fullmatch(value) is not None, code)
    return value


def positive(value, code):
    require(type(value) is int and value > 0, code)
    return value


def exact_equal(actual, expected):
    if type(actual) is not type(expected):
        return False
    if type(expected) is dict:
        return (actual.keys() == expected.keys() and
                all(exact_equal(actual[key], value) for key, value in expected.items()))
    if type(expected) is list:
        return len(actual) == len(expected) and all(map(lambda pair: exact_equal(*pair),
                                                        zip(actual, expected)))
    return actual == expected


def same_fields(actual, expected, code):
    require(type(actual) is dict, code)
    for key, value in expected.items():
        require(key in actual and exact_equal(actual[key], value), code + "-" + key)


def sha256(raw):
    return hashlib.sha256(raw).hexdigest()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate-json-key")
        result[key] = value
    return result


def parse_json(raw):
    require(0 < len(raw) <= MAX_JSON_BYTES, "json-size")
    try:
        result = json.loads(raw.decode("utf-8-sig"), object_pairs_hook=unique_object,
                            parse_constant=lambda _: (_ for _ in ()).throw(
                                ProvenanceError("nonfinite-json-number")))
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise ProvenanceError("invalid-json") from exc
    require(type(result) is dict, "json-object-required")
    return result


def validate_run(run, repo, run_id, workflow, conclusion):
    formatted(repo, REPO, "repository-format")
    formatted(run_id, NUMBER, "run-id-format")
    require(type(run) is dict, "run-object")
    same_fields(run, {"id": int(run_id), "path": ".github/workflows/" + workflow,
                     "event": "workflow_dispatch", "head_branch": "main",
                     "status": "completed", "conclusion": conclusion}, "run")
    for field in ("repository", "head_repository"):
        value = run.get(field)
        require(type(value) is dict and value.get("full_name") == repo, "run-" + field)
        positive(value.get("id"), "run-" + field + "-id")
    require(run["repository"]["id"] == run["head_repository"]["id"], "foreign-head-repository")
    formatted(run.get("head_sha"), SHA, "run-head-sha")
    positive(run.get("run_number"), "run-number")
    positive(run.get("run_attempt"), "run-attempt")


def validate_jobs(jobs, run):
    require(type(jobs) is list, "jobs-list")
    eligible = [job for job in jobs if type(job) is dict and job.get("name") == "Production eligibility"]
    require(len(eligible) == 1, "eligibility-job-unique")
    same_fields(eligible[0], {"run_id": run["id"], "status": "completed",
                             "conclusion": "success", "head_sha": run["head_sha"]},
                "eligibility-job")


def select_artifact(artifacts, run):
    require(type(artifacts) is list, "artifacts-list")
    name = "production-evidence-" + str(run["id"])
    matches = [a for a in artifacts if type(a) is dict and a.get("name") == name]
    require(len(matches) == 1, "original-artifact-unique")
    artifact = matches[0]
    positive(artifact.get("id"), "artifact-id")
    require(artifact.get("expired") is False, "artifact-expired")
    require(positive(artifact.get("size_in_bytes"), "artifact-size") <= MAX_ARCHIVE_BYTES,
            "artifact-size")
    same_fields(artifact.get("workflow_run"), {
        "id": run["id"], "repository_id": run["repository"]["id"],
        "head_repository_id": run["head_repository"]["id"],
        "head_sha": run["head_sha"], "head_branch": "main"}, "artifact-run")
    formatted(artifact.get("digest"), DIGEST, "artifact-digest")
    return artifact


def read_artifact(archive, artifact):
    require(0 < len(archive) <= MAX_ARCHIVE_BYTES, "archive-size")
    require("sha256:" + sha256(archive) == artifact["digest"], "archive-digest")
    try:
        with zipfile.ZipFile(io.BytesIO(archive)) as zipped:
            names = zipped.namelist()
            require(len(names) <= 100, "archive-entry-count")
            raw = {}
            for name in (TOP_NAME, HOST_NAME):
                require(names.count(name) == 1, "archive-required-file-unique")
                info = zipped.getinfo(name)
                require(0 < info.file_size <= MAX_JSON_BYTES, "archive-json-size")
                require(not stat.S_ISLNK(info.external_attr >> 16) and not info.is_dir() and
                        not (info.flag_bits & 1), "archive-regular-unencrypted-file")
                # Read only exact flat names. Never trust paths or extractall.
                raw[name] = zipped.read(info)
            return raw
    except (zipfile.BadZipFile, RuntimeError, NotImplementedError) as exc:
        raise ProvenanceError("invalid-archive") from exc


def validate_indexes(indexes):
    require(type(indexes) is list and len(indexes) == len(INDEXES), "host-indexes")
    require(all(type(row) is dict and type(row.get("name")) is str for row in indexes),
            "host-index-shape")
    require({row["name"] for row in indexes} == set(INDEXES), "host-index-names")
    for row in indexes:
        require(row.get("valid") is True and row.get("ready") is True, "host-index-ready")
        definition = row.get("definition")
        require(type(definition) is str and
                " ".join(definition.replace("public.", "").split()) == INDEXES[row["name"]],
                "host-index-definition")


def validate_original(run, jobs, artifact, raw, repo, run_id, owner, archive_sha256):
    validate_run(run, repo, run_id, "production-deploy.yml", "failure")
    validate_jobs(jobs, run)
    formatted(owner, OWNER, "owner-format")
    top, host = parse_json(raw[TOP_NAME]), parse_json(raw[HOST_NAME])
    same_fields(top, {"schemaVersion": 2, "conclusion": "failure", "workflowRunId": run_id,
                     "operationalSha": run["head_sha"], "failureClass": "controller-unresolved",
                     "rollback": "unknown-host-outcome"}, "original-evidence")
    target = formatted(top.get("targetSha"), SHA, "target-sha")
    previous = formatted(top.get("previousProductionSha"), SHA, "previous-sha")
    require(target != previous, "target-previous-distinct")
    previous_image = formatted(top.get("previousImageDigest"), DIGEST, "previous-image")
    identity = {"schemaVersion": 1, "owner": owner, "unit": "csx-migration-" + owner + ".service",
                "operationalSha": run["head_sha"], "targetSha": target, "servedRevision": target,
                "phase": "committed", "conclusion": "success", "acceptanceAuthority": "host",
                "controllerSmoke": "host-verified", "migrationVerification": "pass",
                "health": "ok", "proxyHealth": "ok", "smoke": "pass",
                "representativeSmoke": "pass", "cleanup": "pass", "rollback": "not-started"}
    same_fields(host, identity, "host-evidence")
    require(not any(key in host for key in ("rollbackFailures", "rollbackServerCleanup")),
            "host-rollback-evidence")
    image = formatted(host.get("imageDigest"), DIGEST, "host-image")
    require(image != previous_image, "target-previous-image-distinct")
    release = formatted(host.get("releaseTag"), RELEASE, "host-release")
    started = formatted(host.get("serverStartedAt"), STARTED, "host-started")
    try:
        datetime.datetime.fromisoformat(started.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ProvenanceError("host-started-calendar") from exc
    ledger = host.get("migrationLedger")
    require(type(ledger) is dict, "host-ledger")
    migration = formatted(ledger.get("version"), MIGRATION, "host-migration")
    count = positive(ledger.get("count"), "host-migration-count")
    # Deliberately narrow: this reconciliation targets the reviewed 0037 state.
    require(migration == "0037_slow_query_indexes.sql" and count == 38, "host-reviewed-ledger")
    budget = positive(host.get("migrationTimeoutSeconds"), "host-migration-budget")
    require(60 <= budget <= 1800, "host-migration-budget")
    same_fields(top, {"migrationBudgetSeconds": budget}, "original-evidence")
    indexes = host.get("indexes")
    validate_indexes(indexes)
    # PowerShell rewrote ancillary phase timestamps. Compare acceptance fields,
    # including the EXACT process start, and preserve/hash the separate raw JSON.
    same_fields(top.get("offlineMigration"),
                dict(identity, imageDigest=image, releaseTag=release, serverStartedAt=started,
                     migrationLedger=ledger, migrationTimeoutSeconds=budget, indexes=indexes),
                "nested-host-evidence")
    tracking = top.get("trackingIssue")
    require(type(tracking) is str, "tracking-issue")
    prefix = "https://github.com/" + repo + "/issues/"
    number = tracking[len(prefix):] if tracking.startswith(prefix) else tracking
    number = number[1:] if number.startswith("#") else number
    formatted(number, NUMBER, "tracking-issue")
    expected = {"schemaVersion": 1, "owner": owner, "operationalSha": run["head_sha"],
                "targetSha": target, "previousSha": previous, "imageDigest": image,
                "previousImageDigest": previous_image, "expectedReleaseTag": release,
                "expectedMigration": migration, "expectedMigrationCount": count,
                "migrationTimeoutSeconds": budget, "serverStartedAt": started,
                "evidenceSha256": sha256(raw[HOST_NAME])}
    provenance = {"schemaVersion": 1, "repository": repo, "owner": owner,
                  "originalDeploymentRunId": run_id, "originalDeploymentRunNumber": run["run_number"],
                  "originalRunAttempt": run["run_attempt"], "originalOperationalSha": run["head_sha"],
                  "originalArtifactId": str(artifact["id"]), "originalArtifactSha256": archive_sha256,
                  "originalEvidenceSha256": sha256(raw[TOP_NAME]),
                  "evidenceSha256": sha256(raw[HOST_NAME]), "trackingIssue": tracking}
    return expected, provenance


def gh_bytes(endpoint, maximum=MAX_JSON_BYTES):
    # Bounded subprocess and disk-backed stdout; do not emit account-bearing stderr.
    with tempfile.TemporaryFile() as output:
        try:
            subprocess.run(["gh", "api", "-H", "Accept: application/vnd.github+json", endpoint],
                           check=True, stdout=output, stderr=subprocess.PIPE,
                           stdin=subprocess.DEVNULL, timeout=API_TIMEOUT)
        except (OSError, subprocess.SubprocessError) as exc:
            raise ProvenanceError("github-api-failed") from exc
        require(0 < output.tell() <= maximum, "github-response-size")
        output.seek(0)
        return output.read(maximum + 1)


def gh_json(endpoint):
    return parse_json(gh_bytes(endpoint))


def gh_collection(endpoint, key):
    # Refuse a truncated listing instead of guessing uniqueness or eligibility.
    response = gh_json(endpoint + ("&" if "?" in endpoint else "?") + "per_page=100")
    rows = response.get(key)
    require(type(rows) is list and type(response.get("total_count")) is int and
            response["total_count"] == len(rows) and len(rows) <= 100,
            "github-collection-incomplete")
    return rows


def fetch_original(repo, run_id, owner):
    formatted(repo, REPO, "repository-format")
    formatted(run_id, NUMBER, "run-id-format")
    formatted(owner, OWNER, "owner-format")
    endpoint = "repos/" + repo + "/actions/runs/" + run_id
    run = gh_json(endpoint)
    validate_run(run, repo, run_id, "production-deploy.yml", "failure")
    jobs = gh_collection(endpoint + "/attempts/" + str(run["run_attempt"]) + "/jobs", "jobs")
    validate_jobs(jobs, run)
    artifact = select_artifact(gh_collection(endpoint + "/artifacts", "artifacts"), run)
    archive = gh_bytes("repos/" + repo + "/actions/artifacts/" + str(artifact["id"]) + "/zip",
                       MAX_ARCHIVE_BYTES)
    raw = read_artifact(archive, artifact)
    expected, provenance = validate_original(run, jobs, artifact, raw, repo, run_id, owner,
                                             sha256(archive))
    return expected, provenance, run, raw, archive


def validate_reconciled(evidence, expected, provenance, run):
    """Cross-check successful reconciliation against freshly downloaded originals."""
    run_id = str(run.get("id", ""))
    validate_run(run, provenance["repository"], run_id, "production-reconcile.yml", "success")
    same_fields(evidence, {
        "schemaVersion": 3, "workflowRunId": run_id, "operationalSha": run["head_sha"],
        "conclusion": "success", "targetSha": expected["targetSha"],
        "deployedSha": expected["targetSha"], "servedRevision": expected["targetSha"],
        "previousProductionSha": expected["previousSha"], "imageDigest": expected["imageDigest"],
        "serverStartedAt": expected["serverStartedAt"], "migrationVersion": expected["expectedMigration"],
        "health": "ok", "smoke": "pass", "rollback": "not-needed", "failureClass": "none",
        "acceptanceAuthority": "host-reconciled", "trackingIssue": provenance["trackingIssue"],
    }, "reconciled-evidence")
    same_fields(evidence.get("reconciliation"), {
        "kind": "committed-host", "originalDeploymentRunId": provenance["originalDeploymentRunId"],
        "originalDeploymentRunNumber": provenance["originalDeploymentRunNumber"],
        "originalOperationalSha": provenance["originalOperationalSha"], "owner": expected["owner"],
        "evidenceSha256": provenance["evidenceSha256"], "originalArtifactId": provenance["originalArtifactId"],
        "originalArtifactSha256": provenance["originalArtifactSha256"],
        "originalEvidenceSha256": provenance["originalEvidenceSha256"], "lockDisposition": "retained",
    }, "reconciliation")
    validate_host_report(evidence.get("hostReconciliation"), expected)
    validate_public_probe(evidence.get("publicBefore"), expected)
    validate_public_probe(evidence.get("publicAfter"), expected)
    return evidence


def validate_public_probe(probe, expected):
    """Share exact public acceptance between the producer and observer."""
    require(exact_equal(probe, {"healthz": "ok", "version": {
        "revision": expected["targetSha"], "version": expected["expectedReleaseTag"]}}),
        "reconciled-public-probe")
    return probe


def validate_host_report(host, expected):
    """Share rich host acceptance between the producer and observer."""
    same_fields(host, dict(expected, conclusion="success", lockDisposition="retained",
                          retainedEvidenceSha256=expected["evidenceSha256"]), "reconciled-host")
    same_fields(host, {"acceptanceAuthority": "host-reconciliation",
                       "migrationLedger": {"version": expected["expectedMigration"],
                                           "count": expected["expectedMigrationCount"]},
                       "servedRevision": expected["targetSha"], "health": "ok", "proxyHealth": "ok",
                       "smoke": "pass", "representativeSmoke": "pass", "cleanup": "pass"},
                "reconciled-host")
    require("failureStage" not in host and "failureCode" not in host, "reconciled-host-failure")
    validate_indexes(host.get("indexes"))
    same_fields(host.get("checks"), {key: "pass" for key in CHECKS}, "reconciled-host-check")
    formatted(host.get("retainedConfigSha256"), HEX, "reconciled-config-hash")
    for key in ("identityBefore", "identityAfter"):
        same_fields(host.get(key), {"imageDigest": expected["imageDigest"],
                                   "configuredRevision": expected["targetSha"],
                                   "serverStartedAt": expected["serverStartedAt"],
                                   "releaseTag": expected["expectedReleaseTag"], "restartCount": 0},
                    "reconciled-" + key)
    for key in ("supervisorBefore", "supervisorAfter"):
        unit = "csx-migration-" + expected["owner"] + ".service"
        same_fields(host.get(key), {"Id": unit, "ActiveState": "inactive", "SubState": "dead",
                                   "MainPID": "0", "ControlPID": "0"}, "reconciled-" + key)
        require(host[key].get("LoadState") in ("loaded", "not-found") and
                host[key].get("ControlGroup") in ("", "/system.slice/" + unit),
                "reconciled-supervisor-state")
    return host


def write_outputs(directory, expected, provenance, run, raw, archive):
    directory.mkdir(parents=True, exist_ok=True)
    outputs = dict(raw)
    outputs["original-artifact.zip"] = archive
    for name, value in (("expected.json", expected), ("provenance.json", provenance),
                        ("source-run.json", run)):
        outputs[name] = (json.dumps(value, indent=2) + "\n").encode("utf-8")
    require(all(not (directory / name).exists() and not (directory / name).is_symlink()
                for name in outputs), "output-already-exists")
    for name, value in outputs.items():
        with (directory / name).open("xb") as output:
            output.write(value)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--owner", required=True)
    parser.add_argument("--output-dir", required=True, type=Path)
    parser.add_argument("--reconciled-evidence", type=Path)
    parser.add_argument("--reconciliation-run", type=Path)
    args = parser.parse_args(argv)
    require(bool(args.reconciled_evidence) == bool(args.reconciliation_run),
            "reconciliation-arguments-paired")
    expected, provenance, run, raw, archive = fetch_original(args.repo, args.run_id, args.owner)
    if args.reconciled_evidence:
        validate_reconciled(parse_json(args.reconciled_evidence.read_bytes()), expected, provenance,
                            parse_json(args.reconciliation_run.read_bytes()))
    write_outputs(args.output_dir, expected, provenance, run, raw, archive)
    print(json.dumps({"conclusion": "success", "originalDeploymentRunId": args.run_id,
                      "owner": args.owner, "targetSha": expected["targetSha"]}))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ProvenanceError, OSError) as error:
        code = str(error) if isinstance(error, ProvenanceError) else "local-file-error"
        print(json.dumps({"conclusion": "failure", "failureCode": code}), file=sys.stderr)
        sys.exit(1)
