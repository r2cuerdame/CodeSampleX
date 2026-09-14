#!/usr/bin/env python3
"""Authenticate pre-activation failed run evidence; recover the retained lock.

Only the exact pre-activation retained-lock class is accepted: failed rollout
with pre-activation failureClass (rollback-critical), null offlineMigration,
empty serverStartedAt, health ok, deployed/served revision and image digest
retaining previous production state.

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
        require(len(entries) == 1 and entries[0].filename == provenance.TOP,
                "artifact must contain only production-deploy-evidence.json")
        entry = entries[0]
        require(not entry.is_dir() and entry.file_size <= 4 * 1024 * 1024 and
                (entry.external_attr >> 16) & 0o170000 in (0, 0o100000),
                "artifact member must be a bounded regular file")
        evidence_bytes = archive.read(entry)
    top = provenance.unique_json(evidence_bytes)
    return artifact, raw_zip, top, evidence_bytes


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
    with urllib.request.urlopen("https://codesamplex.dev/healthz", timeout=15) as response:
        require(response.status == 200 and response.url == "https://codesamplex.dev/healthz" and
                response.read(1024).strip() == b"ok", "public production health is unavailable")


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


def make_final_evidence(top, request, host_result, artifact_digest, top_digest):
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
        "rollback": "not-needed",
        "acceptanceAuthority": "preactivation-recovery",
        "observation": "not-needed",
        "recovery": {
            "recoveryClass": "pre-activation-retained-lock",
            "sourceRunId": request["sourceRunId"],
            "sourceRunAttempt": request["sourceRunAttempt"],
            "sourceArtifactId": request["sourceArtifactId"],
            "sourceArtifactSha256": artifact_digest,
            "sourceEvidenceSha256": top_digest,
            "lockOwner": host_result["owner"],
            "lockArchive": host_result["archive"],
            "recoveredAt": host_result["verifiedAt"],
            "verification": host_result,
        },
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["verify", "release"])
    parser.add_argument("--source-run-id", required=True)
    parser.add_argument("--source-run-attempt", type=int, default=1)
    parser.add_argument("--source-artifact-id", type=int, required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()

    repo = os.environ.get("GITHUB_REPOSITORY", "r2cuerdame/CodeSampleX")
    api = provenance.GitHub(repo)

    run, rollout = authenticate_source_run(api, args.source_run_id, args.source_run_attempt)
    artifact, raw_zip, top, top_bytes = authenticate_source_artifact(api, run, rollout, args.source_artifact_id)
    validate_preactivation_evidence(run, top, repo)

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

    request = {
        "mode": args.mode,
        "repository": repo,
        "sourceRunId": str(args.source_run_id),
        "sourceRunAttempt": args.source_run_attempt,
        "sourceArtifactId": args.source_artifact_id,
        "sourceArtifactSha256": artifact_digest,
        "sourceEvidenceSha256": top_digest,
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
    require(host_result.get("recoveryClass") == "pre-activation-retained-lock", "recoveryClass mismatch")
    require(host_result.get("health") == "ok", "host health not ok")
    require(provenance.matching(host_result.get("owner"), r"[0-9a-f]{32}"), "invalid host owner")

    expected_state = "archived" if args.mode == "release" else "owned"
    require(host_result.get("lockState") == expected_state, f"expected lockState {expected_state}")
    if args.mode == "release":
        receipt = host_result.get("receipt")
        require(isinstance(receipt, dict) and receipt.get("owner") == host_result["owner"],
                "missing or invalid recovery receipt")

    final_evidence = make_final_evidence(top, request, host_result, artifact_digest, top_digest)
    Path(args.output).write_text(json.dumps(final_evidence, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("pre-activation lock recovery refused: " + str(error), file=sys.stderr)
        sys.exit(1)
