#!/usr/bin/env python3
"""Authenticate a fail-closed run and recover its retained deployment lock.

Two exact classes are accepted: the original pre-activation staging failure,
and a pre-migration host rollback proof failure. The latter requires the
separate host evidence to prove quiescence failed before migration began, then
re-proves the unchanged ledger and previous live identity on the host.

The script verifies GitHub run/artifact provenance, validates operational CI,
verifies live production equality without mutation, and coordinates host
atomic archival of the lock under the existing command flock.
"""
import argparse
import base64
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("reconciliation_provenance", ROOT / "reconciliation-provenance.py")
provenance = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(provenance)
require = provenance.require


def authenticate_source_run(api, run_id, attempt):
    require(provenance.matching(str(run_id), r"[1-9][0-9]*"), "source run ID must be positive integer")
    require(provenance.positive(attempt), "source run attempt must be positive integer")
    run = api.api(f"actions/runs/{run_id}/attempts/{attempt}")
    provenance.validate_run(run, api.repository, provenance.DEPLOY, "failure", run_id, attempt)
    jobs = api.pages(f"actions/runs/{run_id}/attempts/{attempt}/jobs", "jobs")
    provenance.exact_job(jobs, "Production eligibility", run, "success")
    rollout = provenance.exact_job(jobs, "Roll out production", run, "failure")
    failures = [s for s in rollout.get("steps", []) if s.get("conclusion") == "failure"]
    require(len(failures) == 1 and failures[0].get("name") == "Deploy and verify",
            "source must fail at deployment verification only")
    return run, rollout


def authenticate_source_artifact(api, run, rollout, artifact_id):
    require(provenance.positive(artifact_id), "source artifact ID must be positive integer")
    artifact = api.api(f"actions/artifacts/{artifact_id}")
    require(type(artifact.get("id")) is int and artifact["id"] == artifact_id and
            artifact.get("name") == f"production-evidence-{run['id']}",
            "source artifact ID or name mismatch")
    require(artifact.get("expired") is False, "source artifact is expired")
    origin = artifact.get("workflow_run", {})
    require(origin.get("id") == run["id"] and origin.get("head_sha") == run["head_sha"] and
            origin.get("head_branch") == "main", "artifact workflow identity mismatch")
    started, completed = provenance.timestamp(rollout["started_at"]), provenance.timestamp(rollout["completed_at"])
    require(started <= provenance.timestamp(artifact["created_at"]) <= completed,
            "artifact was not created within rollout attempt interval")
    raw_zip = api.api(f"actions/artifacts/{artifact_id}/zip", binary=True)
    require("sha256:" + provenance.digest(raw_zip) == artifact.get("digest"),
            "artifact ZIP SHA256 mismatch")
    with zipfile.ZipFile(io.BytesIO(raw_zip)) as archive:
        entries = archive.infolist()
        names = [entry.filename for entry in entries]
        require(len(names) == len(set(names)) and set(names) in
                ({provenance.TOP}, {provenance.TOP, provenance.HOST}),
                "artifact has an unsupported member set")
        for entry in entries:
            require(not entry.is_dir() and entry.file_size <= 4 * 1024 * 1024 and
                    (entry.external_attr >> 16) & 0o170000 in (0, 0o100000),
                    "artifact member must be a bounded regular file")
        evidence_bytes = archive.read(provenance.TOP)
        migration_bytes = archive.read(provenance.HOST) if provenance.HOST in names else None
    top = provenance.unique_json(evidence_bytes)
    expected_names = ({provenance.TOP, provenance.HOST} if
                      isinstance(top, dict) and top.get("failureClass") == "controller-unresolved"
                      else {provenance.TOP})
    require(set(names) == expected_names, "artifact members do not match the failure class")
    migration = provenance.unique_json(migration_bytes) if migration_bytes is not None else None
    return artifact, raw_zip, top, evidence_bytes, migration, migration_bytes


def validate_preactivation_evidence(run, top, repository):
    require(isinstance(top, dict), "evidence must be JSON object")
    require(top.get("schemaVersion") == 2, "evidence schemaVersion must be 2")
    require(top.get("conclusion") == "failure", "evidence conclusion must be failure")
    require(top.get("failureClass") == "rollback-critical", "failureClass must be rollback-critical")
    require(top.get("rollback") == "unverified", "rollback must be unverified")
    require(str(top.get("workflowRunId")) == str(run["id"]), "workflowRunId mismatch")
    require(top.get("operationalSha") == run["head_sha"], "operationalSha mismatch")
    for key in ("targetSha", "previousProductionSha", "operationalSha", "deployedSha", "servedRevision"):
        require(provenance.matching(top.get(key), provenance.SHA), "invalid " + key)
    require(top["targetSha"] != top["previousProductionSha"], "targetSha must differ from previous")
    require(top["deployedSha"] == top["previousProductionSha"], "deployedSha must equal previous (no mutation)")
    require(top["servedRevision"] == top["previousProductionSha"], "servedRevision must equal previous (no mutation)")
    require(provenance.matching(top.get("previousImageDigest"), provenance.DIGEST), "invalid previousImageDigest")
    require(provenance.matching(top.get("imageDigest"), provenance.DIGEST), "invalid imageDigest")
    require(top["imageDigest"] == top["previousImageDigest"], "imageDigest must equal previous (no mutation)")
    require(top.get("offlineMigration") is None, "offlineMigration must be null")
    require(top.get("serverStartedAt") in ("", None), "serverStartedAt must be empty")
    require(top.get("health") == "ok", "health must be ok")
    require(provenance.matching(top.get("trackingIssue"),
            r"(?:#?[1-9][0-9]*|https://github\.com/" + re.escape(repository) + r"/issues/[1-9][0-9]*)"),
            "noncanonical tracking issue")


def validate_premigration_rollback_evidence(run, top, migration, repository):
    require(isinstance(top, dict) and isinstance(migration, dict), "evidence must be JSON objects")
    require(top.get("schemaVersion") == 2 and top.get("conclusion") == "failure",
            "top-level evidence must be a schema-2 failure")
    require(top.get("failureClass") == "controller-unresolved", "failureClass must be controller-unresolved")
    require(top.get("rollback") == "unknown-host-outcome", "rollback must be unknown-host-outcome")
    require(str(top.get("workflowRunId")) == str(run["id"]), "workflowRunId mismatch")
    require(top.get("operationalSha") == run["head_sha"], "operationalSha mismatch")
    for key in ("targetSha", "previousProductionSha", "operationalSha"):
        require(provenance.matching(top.get(key), provenance.SHA), "invalid " + key)
    require(top["targetSha"] != top["previousProductionSha"], "targetSha must differ from previous")
    require(provenance.matching(top.get("previousImageDigest"), provenance.DIGEST),
            "invalid previousImageDigest")
    require(top.get("deployedSha") == "" and top.get("imageDigest") == "" and
            top.get("servedRevision") == "unavailable", "controller must not claim an activated image")
    require(top.get("serverStartedAt") in ("", None) and top.get("health") == "not-started" and
            top.get("smoke") == "not-started", "controller activation evidence must be absent")
    summary = top.get("offlineMigration")
    require(isinstance(summary, dict), "top-level host migration summary is missing")
    # PowerShell's top-level evidence round-trip normalizes ISO timestamp
    # precision (for example .396290 to .39629). Compare the safety-bearing
    # fields exactly and authenticate the separate raw host record in full.
    for key in ("schemaVersion", "owner", "unit", "operationalSha", "targetSha", "imageDigest",
                "migrationTimeoutSeconds", "phase", "conclusion", "backends", "cleanup", "rollback",
                "controllerSmoke", "acceptanceAuthority", "migrationLedgerBefore", "preflight",
                "backendOwnership", "serverStopStarted", "failure", "rollbackServerBackends",
                "rollbackServerCleanup", "lastBackendObservation", "rollbackFailures"):
        require(summary.get(key) == migration.get(key), "top and host migration evidence differ at " + key)
    require(provenance.matching(top.get("trackingIssue"),
            r"(?:#?[1-9][0-9]*|https://github\.com/" + re.escape(repository) + r"/issues/[1-9][0-9]*)"),
            "noncanonical tracking issue")

    owner = migration.get("owner")
    require(migration.get("schemaVersion") == 1 and provenance.matching(owner, r"[0-9a-f]{32}"),
            "invalid host evidence identity")
    require(migration.get("unit") == f"csx-migration-{owner}.service", "host unit identity mismatch")
    require(migration.get("operationalSha") == top["operationalSha"] and
            migration.get("targetSha") == top["targetSha"] and
            provenance.matching(migration.get("imageDigest"), provenance.DIGEST),
            "host deployment identity mismatch")
    require(type(migration.get("migrationTimeoutSeconds")) is int and
            60 <= migration["migrationTimeoutSeconds"] <= 1800,
            "host migration timeout is invalid")
    require(migration.get("phase") == "rollback-failed" and migration.get("conclusion") == "failure" and
            migration.get("rollback") == "failed" and migration.get("failure") == "exact rollback failed",
            "host evidence is not the exact rollback-failed terminal state")
    require(migration.get("preflight") == "pass" and migration.get("serverStopStarted") is True and
            migration.get("backendOwnership") == "explicit-dsn-application-name",
            "host pre-migration evidence is incomplete")
    require(migration.get("backends") == [] and migration.get("cleanup") == "pass" and
            migration.get("rollbackServerCleanup") == "pass" and
            migration.get("lastBackendObservation") == [] and
            migration.get("rollbackFailures") in (["rollback-server.sh"], ["rollback-caddy.sh"]),
            "host cleanup or rollback failure evidence is not exact")
    for forbidden in ("migrationStartedAt", "migrationCompletedAt", "migrationLedger",
                      "migrationVerification", "serverActivationStarted", "servedRevision"):
        require(forbidden not in migration, "migration or activation may have started")
    timings = migration.get("phaseTimings")
    expected_timings = {"preflight", "quiescence", "helperCleanup", "recoveryCleanup",
                        "rollback-server.sh", "rollback-caddy.sh"}
    require(isinstance(timings, dict) and set(timings) == expected_timings and
            timings.get("preflight", {}).get("outcome") == "pass" and
            timings.get("quiescence", {}).get("outcome") == "failure" and
            timings.get("helperCleanup", {}).get("outcome") == "pass" and
            timings.get("recoveryCleanup", {}).get("outcome") == "pass" and
            timings.get("rollback-server.sh", {}).get("outcome") == "failure" and
            timings.get("rollback-caddy.sh", {}).get("outcome") == "pass",
            "host phase sequence does not prove a pre-migration rollback failure")
    ledger = migration.get("migrationLedgerBefore")
    require(isinstance(ledger, dict) and provenance.matching(ledger.get("version"), r"[0-9]{4}_[A-Za-z0-9_]+\.sql") and
            type(ledger.get("count")) is int and ledger["count"] > 0,
            "invalid pre-migration ledger baseline")
    return ledger


def classify_recoverable_evidence(run, top, migration, repository):
    if isinstance(top, dict) and top.get("failureClass") == "rollback-critical":
        validate_preactivation_evidence(run, top, repository)
        require(migration is None, "pre-activation recovery cannot include host migration evidence")
        return "pre-activation-retained-lock", None
    validate_premigration_rollback_evidence(run, top, migration, repository)
    return "pre-migration-rollback-failed-retained-lock", migration["migrationLedgerBefore"]


def validate_ancestry(target_sha, operational_sha):
    for sha in (target_sha, operational_sha):
        res = subprocess.run(["git", "merge-base", "--is-ancestor", sha, "origin/main"],
                             capture_output=True, check=False)
        if res.returncode != 0:
            res_local = subprocess.run(["git", "merge-base", "--is-ancestor", sha, "main"],
                                       capture_output=True, check=False)
            require(res_local.returncode == 0, f"commit {sha} is not an ancestor of main")


def validate_operational_ci(api, operational_sha):
    runs = api.api(f"actions/workflows/ci.yml/runs?head_sha={operational_sha}&branch=main&status=success&per_page=100")
    items = runs.get("workflow_runs", [])
    valid_runs = [r for r in items if r.get("path") == ".github/workflows/ci.yml" and
                  r.get("head_branch") == "main" and r.get("event") in ("push", "workflow_dispatch") and
                  r.get("conclusion") == "success"]
    require(len(valid_runs) > 0, f"no successful canonical main CI for {operational_sha}")


def public_health():
    consecutive = 0
    for attempt in range(6):
        try:
            with urllib.request.urlopen("https://codesamplex.dev/healthz", timeout=5) as response:
                healthy = (response.status == 200 and
                           response.url == "https://codesamplex.dev/healthz" and
                           response.read(1024).strip() == b"ok")
        except Exception:
            healthy = False
        consecutive = consecutive + 1 if healthy else 0
        if consecutive == 3:
            return
        if attempt < 5:
            time.sleep(1)
    require(False, "public production health did not pass three consecutive bounded checks")


def remote(request, host, user, key, known_hosts):
    require(provenance.matching(host, r"[A-Za-z0-9][A-Za-z0-9.-]*") and
            provenance.matching(user, r"[a-z_][a-z0-9_-]{0,31}"), "invalid production SSH endpoint")
    for path in (key, known_hosts):
        require(Path(path).is_file(), "required pinned SSH material missing")
    payload = base64.b64encode(json.dumps(request, separators=(",", ":")).encode()).decode()
    command = "flock -w 5 ~/.csx-deploy-command.lock timeout --kill-after=5s 180s python3 - " + shlex.quote(payload)
    process = subprocess.run(["ssh", "-i", str(Path(key).resolve()), "-o", "BatchMode=yes",
                              "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
                              "-o", "UserKnownHostsFile=" + str(Path(known_hosts).resolve()),
                              "-o", "ConnectTimeout=15", user + "@" + host, command],
                             input=(ROOT / "recover-deploy-lock-host.py").read_bytes(),
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=200, check=False)
    require(process.returncode == 0,
            f"fresh host lock recovery refused (SSH exit {process.returncode}): {process.stderr.decode('utf-8', errors='replace').strip()}")
    require(len(process.stdout) <= 2 * 1024 * 1024, "host evidence exceeds size bound")
    return provenance.unique_json(process.stdout)


def make_final_evidence(top, request, host_result, artifact_digest, top_digest, migration_digest):
    return {
        "schemaVersion": 3,
        "conclusion": "success",
        "workflowRunId": request["recoveryRunId"],
        "workflowRunAttempt": request["recoveryRunAttempt"],
        "workflowRunUrl": f"https://github.com/{request['repository']}/actions/runs/{request['recoveryRunId']}",
        "operationalSha": request["operationalSha"],
        "targetSha": top["targetSha"],
        "previousProductionSha": top["previousProductionSha"],
        "previousImageDigest": top["previousImageDigest"],
        "deployedSha": top["previousProductionSha"],
        "servedRevision": top["previousProductionSha"],
        "imageDigest": top["previousImageDigest"],
        "serverStartedAt": host_result.get("containerStartedAt", ""),
        "trackingIssue": top.get("trackingIssue", ""),
        "health": "ok",
        "smoke": "not-needed",
        "rollback": "verified-previous-production",
        "acceptanceAuthority": "retained-lock-recovery",
        "observation": "not-needed",
        "recovery": {
            "recoveryClass": request["recoveryClass"],
            "sourceRunId": request["sourceRunId"],
            "sourceRunAttempt": request["sourceRunAttempt"],
            "sourceArtifactId": request["sourceArtifactId"],
            "sourceArtifactSha256": artifact_digest,
            "sourceEvidenceSha256": top_digest,
            "sourceMigrationEvidenceSha256": migration_digest,
            "lockOwner": host_result["owner"],
            "lockArchive": host_result["archive"],
            "recoveredAt": host_result["verifiedAt"],
            "verification": host_result,
        },
    }


def main(api=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["verify", "release"])
    parser.add_argument("--source-run-id", required=True)
    parser.add_argument("--source-run-attempt", type=int, default=1)
    parser.add_argument("--source-artifact-id", type=int, required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()

    repo = os.environ.get("GITHUB_REPOSITORY", "r2cuerdame/CodeSampleX")
    if api is None:
        api = provenance.GitHub(repo)

    run, rollout = authenticate_source_run(api, args.source_run_id, args.source_run_attempt)
    artifact, raw_zip, top, top_bytes, migration, migration_bytes = authenticate_source_artifact(
        api, run, rollout, args.source_artifact_id)
    recovery_class, migration_ledger_before = classify_recoverable_evidence(run, top, migration, repo)

    operational_sha = os.environ.get("GITHUB_SHA")
    if not operational_sha:
        operational_sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()

    validate_ancestry(top["targetSha"], top["operationalSha"])
    validate_operational_ci(api, operational_sha)

    public_health()

    artifact_digest = artifact["digest"]
    if artifact_digest.startswith("sha256:"):
        artifact_digest = artifact_digest[7:]
    top_digest = provenance.digest(top_bytes)
    migration_digest = provenance.digest(migration_bytes) if migration_bytes is not None else None

    request = {
        "mode": args.mode,
        "repository": repo,
        "sourceRunId": str(args.source_run_id),
        "sourceRunAttempt": args.source_run_attempt,
        "sourceArtifactId": args.source_artifact_id,
        "sourceArtifactSha256": artifact_digest,
        "sourceEvidenceSha256": top_digest,
        "sourceMigrationEvidenceSha256": migration_digest,
        "recoveryClass": recovery_class,
        "migrationLedgerBefore": migration_ledger_before,
        "expectedLockOwner": migration.get("owner") if migration is not None else None,
        "targetSha": top["targetSha"],
        "previousProductionSha": top["previousProductionSha"],
        "previousImageDigest": top["previousImageDigest"],
        "recoveryRunId": os.environ.get("GITHUB_RUN_ID", "1"),
        "recoveryRunAttempt": int(os.environ.get("GITHUB_RUN_ATTEMPT", 1)),
        "operationalSha": operational_sha,
    }

    host = os.environ["PRODUCTION_HOST"]
    user = os.environ.get("PRODUCTION_USER") or "ubuntu"
    key_path = os.environ["PRODUCTION_KEY_PATH"]
    known_hosts_path = os.environ["PRODUCTION_KNOWN_HOSTS_PATH"]

    host_result = remote(request, host, user, key_path, known_hosts_path)
    require(host_result.get("schemaVersion") == 1, "host schemaVersion mismatch")
    require(host_result.get("recoveryClass") == recovery_class, "recoveryClass mismatch")
    require(host_result.get("health") == "ok", "host health not ok")
    require(provenance.matching(host_result.get("owner"), r"[0-9a-f]{32}"), "invalid host owner")
    require(host_result.get("migrationLedger") == migration_ledger_before, "host migration ledger mismatch")

    expected_state = "archived" if args.mode == "release" else "owned"
    require(host_result.get("lockState") == expected_state, f"expected lockState {expected_state}")
    if args.mode == "release":
        receipt = host_result.get("receipt")
        require(isinstance(receipt, dict) and receipt.get("owner") == host_result["owner"],
                "missing or invalid recovery receipt")

    final_evidence = make_final_evidence(top, request, host_result, artifact_digest, top_digest, migration_digest)
    Path(args.output).write_text(json.dumps(final_evidence, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("retained deployment lock recovery refused: " + str(error), file=sys.stderr)
        sys.exit(1)
