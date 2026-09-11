#!/usr/bin/env python3
"""Revalidate and release one committed owner without activating any payload."""
import argparse
import base64
import importlib.util
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import urllib.request

ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("reconciliation_provenance", ROOT / "reconciliation-provenance.py")
provenance = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(provenance)
require = provenance.require


def make_request(bundle, operational_sha, run_id, attempt):
    require(provenance.matching(operational_sha, provenance.SHA) and
            provenance.matching(run_id, r"[1-9][0-9]*") and provenance.positive(attempt),
            "invalid reconciliation controller identity")
    run, artifact, top = bundle["sourceRun"], bundle["sourceArtifact"], bundle["sourceEvidence"]
    return {"mode": "verify", "repository": bundle["repository"],
            "sourceRunId": str(run["id"]), "sourceRunAttempt": run["run_attempt"],
            "sourceArtifactId": artifact["id"], "sourceArtifactSha256": artifact["digest"][7:],
            "hostEvidenceSha256": bundle["hostEvidenceSha256"], "hostEvidence": bundle["hostEvidence"],
            "previousSha": top["previousProductionSha"], "previousImageDigest": top["previousImageDigest"],
            "reconciliationRunId": run_id, "reconciliationRunAttempt": attempt, "operationalSha": operational_sha}


def expected_binding(request):
    host = request["hostEvidence"]
    binding = {key: request[key] for key in ("repository", "sourceRunId", "sourceRunAttempt",
                "sourceArtifactId", "sourceArtifactSha256", "hostEvidenceSha256", "previousSha", "previousImageDigest")}
    binding.update({key: host[key] for key in ("owner", "targetSha", "operationalSha", "imageDigest",
                                             "serverStartedAt", "migrationLedger", "releaseTag")})
    return binding


def validate_result(result, request, released=False):
    require(result.get("schemaVersion") == 1 and
            all(result.get(key) == value for key, value in {"health": "ok", "smoke": "pass", "cleanup": "pass"}.items()),
            "host did not pass fresh reconciliation acceptance")
    binding = result.get("binding", {})
    require(all(binding.get(k) == v for k, v in expected_binding(request).items()), "host source binding mismatch")
    require(result.get("lockState") in (("archived",) if released else ("owned", "archived")),
            "host owner release state is incomplete")
    provenance.timestamp(result["verifiedAt"])
    if released:
        receipt = result.get("receipt") or {}
        require(receipt.get("binding") == binding and receipt.get("preparedArtifact") == request["preparedArtifact"],
                "host release receipt mismatch")


def authenticate_receipt(api, result, request):
    receipt = result.get("receipt")
    if receipt is None:
        require(result["lockState"] == "owned", "missing archive receipt")
        return None
    require(receipt.get("binding") == result.get("binding"), "retained receipt binding mismatch")
    prepared = provenance.authenticate_prepared(api, receipt.get("preparedArtifact", {}))
    old_request = prepared.get("request", {})
    validate_result(prepared.get("verification", {}), old_request)
    require(expected_binding(old_request) == expected_binding(request) and
            prepared["verification"]["binding"] == receipt["binding"] and
            receipt.get("reconciliationRunId") == old_request.get("reconciliationRunId") and
            receipt.get("reconciliationRunAttempt") == old_request.get("reconciliationRunAttempt") and
            receipt.get("operationalSha") == old_request.get("operationalSha"),
            "retained receipt has no matching published preparation")
    return receipt["preparedArtifact"]


def remote(request, host, user, key, known_hosts):
    require(provenance.matching(host, r"[A-Za-z0-9][A-Za-z0-9.-]*") and
            provenance.matching(user, r"[a-z_][a-z0-9_-]{0,31}"), "invalid production SSH endpoint")
    for path in (key, known_hosts):
        require(Path(path).is_file(), "required pinned SSH material missing")
    payload = base64.b64encode(json.dumps(request, separators=(",", ":")).encode()).decode()
    # Match deploy.ps1's host-wide command lock. A detached stdin in host
    # subprocesses prevents them consuming the streamed Python source.
    command = "flock -w 5 ~/.csx-deploy-command.lock timeout --kill-after=5s 180s python3 - " + shlex.quote(payload)
    process = subprocess.run(["ssh", "-i", str(Path(key).resolve()), "-o", "BatchMode=yes",
                              "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
                              "-o", "UserKnownHostsFile=" + str(Path(known_hosts).resolve()),
                              "-o", "ConnectTimeout=15", user + "@" + host, command],
                             input=(ROOT / "reconcile-host.py").read_bytes(), stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE, timeout=200, check=False)
    require(process.returncode == 0, "fresh host reconciliation refused; owner evidence retained (SSH exit {})".format(process.returncode))
    require(len(process.stdout) <= 2 * 1024 * 1024, "host evidence exceeds size bound")
    return provenance.unique_json(process.stdout)


def public_health():
    # A fresh public probe supplements host-local TLS checks; no DNS override,
    # certificate bypass, cached artifact or redirect can satisfy this gate.
    with urllib.request.urlopen("https://codesamplex.dev/healthz", timeout=15) as response:
        require(response.status == 200 and response.url == "https://codesamplex.dev/healthz" and
                response.read(1024).strip() == b"ok", "public production health is unavailable")


def evidence(bundle, request, result):
    top, host, run = bundle["sourceEvidence"], bundle["hostEvidence"], bundle["sourceRun"]
    return {"schemaVersion": 3, "conclusion": "success", "workflowRunId": request["reconciliationRunId"],
            "workflowRunAttempt": request["reconciliationRunAttempt"],
            "workflowRunUrl": "https://github.com/{}/actions/runs/{}".format(bundle["repository"], request["reconciliationRunId"]),
            "operationalSha": request["operationalSha"], "targetSha": host["targetSha"],
            "previousProductionSha": top["previousProductionSha"], "previousImageDigest": top["previousImageDigest"],
            "deployedSha": host["targetSha"], "servedRevision": host["targetSha"], "imageDigest": host["imageDigest"],
            "serverStartedAt": host["serverStartedAt"], "migrationVersion": host["migrationLedger"]["version"],
            "trackingIssue": top["trackingIssue"], "health": "ok", "smoke": "pass", "rollback": "not-needed",
            "acceptanceAuthority": "host-reconciliation", "observation": "pending-independent-workflow",
            "reconciliation": {"sourceRunId": str(run["id"]), "sourceRunAttempt": run["run_attempt"],
                               "sourceRunNumber": run["run_number"], "sourceArtifactId": request["sourceArtifactId"],
                               "sourceArtifactSha256": request["sourceArtifactSha256"],
                               "hostEvidenceSha256": request["hostEvidenceSha256"], "verification": result}}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["verify", "release"])
    parser.add_argument("--bundle", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--prepared")
    parser.add_argument("--prepared-artifact-id", type=int)
    parser.add_argument("--prepared-artifact-digest")
    args = parser.parse_args()
    bundle = provenance.unique_json(Path(args.bundle).read_bytes())
    api = provenance.GitHub(os.environ["GITHUB_REPOSITORY"])
    # Re-fetch the originally selected attempt at each stage. Later production
    # reruns cannot replace its evidence; changed original evidence still fails.
    authenticated = provenance.fetch_source(api, str(bundle["sourceRun"]["id"]), bundle["sourceRun"]["run_attempt"])
    require(authenticated == bundle, "source provenance changed since eligibility")
    request = make_request(bundle, os.environ["GITHUB_SHA"], os.environ["GITHUB_RUN_ID"], int(os.environ["GITHUB_RUN_ATTEMPT"]))
    if args.mode == "release":
        require(args.prepared and args.prepared_artifact_id and args.prepared_artifact_digest,
                "release requires uploaded preparation")
        uploaded_digest = args.prepared_artifact_digest
        if provenance.matching(uploaded_digest, r"[0-9a-f]{64}"):
            uploaded_digest = "sha256:" + uploaded_digest
        proof = {"id": args.prepared_artifact_id, "digest": uploaded_digest,
                 "runId": request["reconciliationRunId"], "runAttempt": request["reconciliationRunAttempt"]}
        published = provenance.authenticate_prepared(api, proof)
        local_prepared = provenance.unique_json(Path(args.prepared).read_bytes())
        require(published == local_prepared and published.get("request") == request,
                "published preparation differs from this controller's verification")
        validate_result(published["verification"], request)
        retained = authenticate_receipt(api, published["verification"], request)
        request["mode"] = "release"
        request["preparedArtifact"] = retained or proof
    public_health()
    result = remote(request, os.environ["PRODUCTION_HOST"], os.environ.get("PRODUCTION_USER") or "ubuntu",
                    os.environ["PRODUCTION_KEY_PATH"], os.environ["PRODUCTION_KNOWN_HOSTS_PATH"])
    validate_result(result, request, released=args.mode == "release")
    authenticate_receipt(api, result, request)
    value = ({"schemaVersion": 1, "request": request, "verification": result} if args.mode == "verify"
             else evidence(bundle, request, result))
    Path(args.output).write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, subprocess.TimeoutExpired) as error:
        print("production reconciliation refused: " + str(error), file=sys.stderr)
        sys.exit(1)
