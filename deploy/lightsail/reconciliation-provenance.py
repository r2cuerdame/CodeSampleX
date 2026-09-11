#!/usr/bin/env python3
"""Authenticate committed-owner reconciliation using read-only GitHub evidence.

ZIPs are hashed before reading. Only exact named regular JSON members are read;
no archive member is extracted or executed. All GitHub calls have finite caps.
"""
import argparse
import datetime
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import zipfile

SHA = r"[0-9a-f]{40}"
DIGEST = r"sha256:[0-9a-f]{64}"
START = r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z"
DEPLOY = ".github/workflows/production-deploy.yml"
RECONCILE = ".github/workflows/production-reconciliation.yml"
TOP = "production-deploy-evidence.json"
HOST = TOP + ".migration.json"
PREPARED = "production-reconciliation-prepared.json"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def matching(value, pattern):
    return isinstance(value, str) and re.fullmatch(pattern, value) is not None


def positive(value):
    return type(value) is int and value > 0


def unique_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "duplicate JSON key")
            result[key] = value
        return result

    def constant(value):
        raise ValueError("non-finite JSON number")

    return json.loads(raw.decode("utf-8-sig") if isinstance(raw, bytes) else raw,
                      object_pairs_hook=pairs, parse_constant=constant)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def timestamp(value):
    require(matching(value, r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|\+00:00)"),
            "timestamp must be UTC RFC3339")
    # Python 3.8 accepts fewer ISO fraction formats. Compare exact integer ns,
    # retaining original identity strings in all published evidence.
    seconds = int(datetime.datetime.fromisoformat(value[:19] + "+00:00").timestamp())
    fraction = re.search(r"\.(\d+)", value)
    return seconds * 1_000_000_000 + (int(fraction[1].ljust(9, "0")) if fraction else 0)


class GitHub:
    def __init__(self, repository):
        require(matching(repository, r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"), "invalid repository")
        self.repository = repository

    def api(self, path, binary=False):
        result = subprocess.run(["gh", "api", "-H", "Accept: application/vnd.github+json",
                                 "repos/" + self.repository + "/" + path],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60, check=False)
        require(result.returncode == 0, "GitHub evidence request failed: " + path.split("?")[0])
        require(len(result.stdout) <= 16 * 1024 * 1024, "GitHub evidence exceeds size bound")
        return result.stdout if binary else unique_json(result.stdout)

    def pages(self, path, key):
        records = []
        for page in range(1, 11):
            result = self.api(path + ("&" if "?" in path else "?") + "per_page=100&page=" + str(page))
            items = result[key]
            require(isinstance(items, list), "malformed GitHub list")
            records.extend(items)
            if len(items) < 100:
                return records
        raise ValueError("GitHub pagination exceeds limit")


def validate_run(run, repository, path, conclusion=None, run_id=None, attempt=None):
    require(run.get("path") == path and run.get("event") == "workflow_dispatch" and
            run.get("head_branch") == "main" and
            run.get("repository", {}).get("full_name") == repository and
            run.get("head_repository", {}).get("full_name") == repository,
            "run is not a canonical main workflow dispatch")
    require(positive(run.get("id")) and positive(run.get("run_number")) and
            positive(run.get("run_attempt")) and matching(run.get("head_sha"), SHA), "run identity incomplete")
    if conclusion is not None:
        require(run.get("status") == "completed" and run.get("conclusion") == conclusion,
                "workflow conclusion does not authorize evidence")
    if run_id is not None:
        require(str(run["id"]) == str(run_id), "workflow ID mismatch")
    if attempt is not None:
        require(run["run_attempt"] == attempt, "workflow attempt mismatch")


def exact_job(jobs, name, run, conclusion):
    selected = [job for job in jobs if job.get("name") == name]
    require(len(selected) == 1, "expected exactly one " + name + " job")
    job = selected[0]
    require(job.get("run_id") == run["id"] and job.get("run_attempt") == run["run_attempt"] and
            job.get("status") == "completed" and job.get("conclusion") == conclusion,
            "job attempt or conclusion mismatch")
    require(timestamp(job["started_at"]) <= timestamp(job["completed_at"]), "invalid job interval")
    return job


def artifact_files(api, artifact, names, run):
    require(positive(artifact.get("id")) and artifact.get("expired") is False and
            matching(artifact.get("digest"), DIGEST), "artifact needs immutable SHA256 provenance")
    origin = artifact.get("workflow_run", {})
    require(origin.get("id") == run["id"] and origin.get("head_sha") == run["head_sha"] and
            origin.get("head_branch") == "main", "artifact workflow identity mismatch")
    raw = api.api("actions/artifacts/" + str(artifact["id"]) + "/zip", binary=True)
    require("sha256:" + digest(raw) == artifact["digest"], "artifact ZIP SHA256 mismatch")
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        entries = archive.infolist()
        require(len(entries) == len(names) and set(x.filename for x in entries) == set(names),
                "artifact members must be exact and unique")
        require(all(not entry.is_dir() and entry.file_size <= 4 * 1024 * 1024 and
                    (entry.external_attr >> 16) & 0o170000 in (0, 0o100000) for entry in entries),
                "artifact members must be bounded regular files")
        return {name: archive.read(name) for name in names}


def named_artifact(api, run, name):
    artifacts = api.pages("actions/runs/" + str(run["id"]) + "/artifacts", "artifacts")
    selected = [a for a in artifacts if a.get("name") == name and a.get("expired") is False]
    require(len(selected) == 1, "expected exactly one unexpired " + name + " artifact")
    return selected[0]


def validate_source(run, jobs, artifact, top_raw, host_raw, repository):
    validate_run(run, repository, DEPLOY, "failure")
    exact_job(jobs, "Production eligibility", run, "success")
    rollout = exact_job(jobs, "Roll out production", run, "failure")
    require(timestamp(rollout["started_at"]) <= timestamp(artifact["created_at"]) <=
            timestamp(rollout["completed_at"]), "artifact belongs to another deployment attempt")
    failures = [s for s in rollout.get("steps", []) if s.get("conclusion") == "failure"]
    require(len(failures) == 1 and failures[0].get("name") == "Deploy and verify",
            "source must fail at deployment verification only")
    top, host = unique_json(top_raw), unique_json(host_raw)
    require(isinstance(top, dict) and isinstance(host, dict), "source evidence must be JSON objects")
    require(top.get("schemaVersion") == 2 and top.get("conclusion") == "failure" and
            top.get("failureClass") == "controller-unresolved" and
            top.get("rollback") == "unknown-host-outcome" and
            str(top.get("workflowRunId")) == str(run["id"]) and top.get("operationalSha") == run["head_sha"],
            "source is not an unresolved canonical controller")
    for key in ("targetSha", "previousProductionSha", "operationalSha"):
        require(matching(top.get(key), SHA), "invalid source " + key)
    require(top["targetSha"] != top["previousProductionSha"] and matching(top.get("previousImageDigest"), DIGEST),
            "invalid previous deployment identity")
    require(matching(top.get("trackingIssue"), r"(?:#?[1-9][0-9]*|https://github\.com/" +
                     re.escape(repository) + r"/issues/[1-9][0-9]*)"), "noncanonical tracking issue")
    expected = {"schemaVersion": 1, "phase": "committed", "conclusion": "success", "acceptanceAuthority": "host",
                "controllerSmoke": "host-verified", "targetSha": top["targetSha"], "operationalSha": top["operationalSha"],
                "servedRevision": top["targetSha"], "health": "ok", "proxyHealth": "ok", "smoke": "pass",
                "representativeSmoke": "pass", "cleanup": "pass", "migrationVerification": "pass", "rollback": "not-started"}
    require(all(host.get(k) == v for k, v in expected.items()), "host did not commit exact acceptance")
    require(matching(host.get("owner"), r"[0-9a-f]{32}") and
            host.get("unit") == "csx-migration-" + host["owner"] + ".service" and
            matching(host.get("imageDigest"), DIGEST) and matching(host.get("serverStartedAt"), START) and
            matching(host.get("releaseTag"), r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"),
            "host identity incomplete")
    ledger = host.get("migrationLedger", {})
    require(isinstance(ledger, dict) and matching(ledger.get("version"), r"[0-9]{4}_[a-z0-9_]+\.sql") and positive(ledger.get("count")),
            "migration ledger incomplete")
    # The old PowerShell controller normalized OTHER embedded timestamps. The
    # original separate host JSON remains the byte authority; compare only the
    # exact acceptance identity in the embedded copy, never reserialized blobs.
    embedded = top.get("offlineMigration", {})
    for key in set(expected) | {"owner", "unit", "imageDigest", "serverStartedAt", "releaseTag", "migrationLedger"}:
        require(embedded.get(key) == host.get(key), "embedded host acceptance mismatch: " + key)
    require(timestamp(rollout["started_at"]) <= timestamp(host["activationStartedAt"]) <= timestamp(host["serverStartedAt"]) <=
            timestamp(host["completedAt"]) <= timestamp(rollout["completed_at"]), "activation outside original job")
    return {"schemaVersion": 1, "repository": repository, "sourceRun": run, "sourceArtifact": artifact,
            "sourceEvidenceSha256": digest(top_raw), "hostEvidenceSha256": digest(host_raw),
            "sourceEvidence": top, "hostEvidence": host}


def fetch_source(api, run_id):
    require(matching(str(run_id), r"[1-9][0-9]*"), "source run ID must be positive")
    run = api.api("actions/runs/" + str(run_id))
    validate_run(run, api.repository, DEPLOY, "failure", run_id)
    jobs = api.pages("actions/runs/{}/attempts/{}/jobs".format(run_id, run["run_attempt"]), "jobs")
    artifact = named_artifact(api, run, "production-evidence-" + str(run_id))
    files = artifact_files(api, artifact, [TOP, HOST], run)
    return validate_source(run, jobs, artifact, files[TOP], files[HOST], api.repository)


def authenticate_prepared(api, proof):
    require(positive(proof.get("id")) and positive(proof.get("runAttempt")) and
            matching(proof.get("runId"), r"[1-9][0-9]*") and matching(proof.get("digest"), DIGEST),
            "prepared proof malformed")
    # Exact historical attempts remain valid preparation when final publication
    # fails. Only the separate FINAL observer source must be successful.
    run = api.api("actions/runs/{}/attempts/{}".format(proof["runId"], proof["runAttempt"]))
    validate_run(run, api.repository, RECONCILE, run_id=proof["runId"], attempt=proof["runAttempt"])
    artifact = api.api("actions/artifacts/" + str(proof["id"]))
    require(artifact.get("digest") == proof["digest"] and artifact.get("name") ==
            "production-reconciliation-prepared-{}-{}".format(proof["runId"], proof["runAttempt"]),
            "prepared artifact ID/digest/name mismatch")
    prepared = unique_json(artifact_files(api, artifact, [PREPARED], run)[PREPARED])
    request = prepared.get("request", {})
    require(request.get("repository") == api.repository and request.get("reconciliationRunId") == proof["runId"] and
            request.get("reconciliationRunAttempt") == proof["runAttempt"] and
            request.get("operationalSha") == run["head_sha"] and request.get("mode") == "verify",
            "prepared document provenance mismatch")
    return prepared


def source_binding(source):
    run, artifact, host, top = (source[k] for k in ("sourceRun", "sourceArtifact", "hostEvidence", "sourceEvidence"))
    result = {"repository": source["repository"], "sourceRunId": str(run["id"]), "sourceRunAttempt": run["run_attempt"],
              "sourceArtifactId": artifact["id"], "sourceArtifactSha256": artifact["digest"][7:],
              "hostEvidenceSha256": source["hostEvidenceSha256"], "previousSha": top["previousProductionSha"],
              "previousImageDigest": top["previousImageDigest"]}
    result.update({k: host[k] for k in ("owner", "targetSha", "operationalSha", "imageDigest", "serverStartedAt",
                                      "migrationLedger", "releaseTag")})
    return result


def validate_observation(api, run, evidence):
    require(evidence.get("schemaVersion") == 3 and positive(evidence.get("workflowRunAttempt")) and
            matching(evidence.get("workflowRunId"), r"[1-9][0-9]*"), "reconciliation run identity missing")
    validate_run(run, api.repository, RECONCILE, "success", evidence.get("workflowRunId"), evidence.get("workflowRunAttempt"))
    require(evidence.get("conclusion") == "success" and evidence.get("operationalSha") == run["head_sha"],
            "reconciliation output did not succeed")
    jobs = api.pages("actions/runs/{}/attempts/{}/jobs".format(run["id"], run["run_attempt"]), "jobs")
    job = exact_job(jobs, "Reconcile committed production owner", run, "success")
    recon = evidence.get("reconciliation", {})
    source = fetch_source(api, recon.get("sourceRunId"))
    original = source["sourceRun"]
    expected = source_binding(source)
    require(recon.get("sourceRunNumber") == original["run_number"] and
            all(recon.get(k) == expected[k] for k in ("sourceRunId", "sourceRunAttempt", "sourceArtifactId",
                "sourceArtifactSha256", "hostEvidenceSha256")), "reconciliation source provenance mismatch")
    verification = recon.get("verification", {})
    receipt = verification.get("receipt") or {}
    require(verification.get("schemaVersion") == 1 and verification.get("lockState") == "archived" and
            all(verification.get(k) == v for k, v in {"health": "ok", "smoke": "pass", "cleanup": "pass"}.items()),
            "no completed owner release")
    require(timestamp(job["started_at"]) <= timestamp(verification["verifiedAt"]) <= timestamp(job["completed_at"]),
            "fresh verification is outside reconciliation job")
    prepared = authenticate_prepared(api, receipt.get("preparedArtifact", {}))
    request, prior = prepared.get("request", {}), prepared.get("verification", {})
    require(prior.get("schemaVersion") == 1 and prior.get("lockState") in ("owned", "archived") and
            all(prior.get(k) == v for k, v in {"health": "ok", "smoke": "pass", "cleanup": "pass"}.items()),
            "prepared verification did not pass")
    for key, value in expected.items():
        candidate = request.get("hostEvidence", {}).get(key) if key in ("owner", "targetSha", "operationalSha", "imageDigest",
                        "serverStartedAt", "migrationLedger", "releaseTag") else request.get(key)
        require(candidate == value, "prepared source identity mismatch: " + key)
    require(prior.get("binding") == verification.get("binding") == receipt.get("binding") == expected,
            "owner release does not match durable preparation")
    for key in ("reconciliationRunId", "reconciliationRunAttempt", "operationalSha"):
        require(receipt.get(key) == request.get(key), "receipt controller provenance mismatch")
    host, top = source["hostEvidence"], source["sourceEvidence"]
    fields = {"targetSha": host["targetSha"], "deployedSha": host["targetSha"], "servedRevision": host["targetSha"],
              "previousProductionSha": top["previousProductionSha"], "imageDigest": host["imageDigest"],
              "serverStartedAt": host["serverStartedAt"], "migrationVersion": host["migrationLedger"]["version"],
              "trackingIssue": top["trackingIssue"], "health": "ok", "smoke": "pass", "rollback": "not-needed"}
    require(all(evidence.get(k) == v for k, v in fields.items()), "reconciled deployment identity mismatch")
    # run_number is workflow-scoped. Keep original production ordering.
    return original["run_number"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["fetch-source", "verify-observation", "download-observation"])
    parser.add_argument("--repository", default=os.environ.get("GITHUB_REPOSITORY", ""))
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--output")
    parser.add_argument("--evidence")
    args = parser.parse_args()
    api = GitHub(args.repository)
    if args.mode == "fetch-source":
        require(args.output is not None, "output is required")
        Path(args.output).write_text(json.dumps(fetch_source(api, args.run_id), indent=2) + "\n", encoding="utf-8")
    else:
        run = api.api("actions/runs/" + args.run_id)
        validate_run(run, api.repository, RECONCILE, "success", args.run_id)
        # Authenticate the exact attempt-qualified final artifact too. An input
        # pathname is a copy to compare, never the authority for observation.
        artifact = named_artifact(api, run, "production-evidence-{}-{}".format(run["id"], run["run_attempt"]))
        actual = unique_json(artifact_files(api, artifact, [TOP], run)[TOP])
        number = validate_observation(api, run, actual)
        if args.mode == "download-observation":
            require(args.output is not None, "authenticated output path required")
            Path(args.output).write_text(json.dumps(actual, indent=2) + "\n", encoding="utf-8")
        else:
            require(actual == unique_json(Path(args.evidence).read_bytes()), "downloaded reconciliation evidence mismatch")
        print(number)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, subprocess.TimeoutExpired, zipfile.BadZipFile) as error:
        print("reconciliation provenance refused: " + str(error), file=sys.stderr)
        sys.exit(1)
