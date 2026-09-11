#!/usr/bin/env python3
"""Read-only controller for an exact deployment committed by a lost controller.

The original failed GitHub run is never rewritten. A separate successful run
attests fresh acceptance and leaves the original owner lock as a deployment
fence. No deploy, rollback, restart, or lock-release operation exists here.
"""
import argparse
import base64
import datetime
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import re
import subprocess
import sys

_spec = importlib.util.spec_from_file_location(
    "reconciliation_validation", Path(__file__).with_name("reconciliation-provenance.py"))
validation = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(validation)


def utc():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def public_probe(host, expected):
    results = {}
    for route in ("healthz", "version"):
        command = ["curl", "--silent", "--show-error", "--fail", "--noproxy", "*",
                   "--connect-timeout", "5", "--max-time", "15",
                   "--resolve", "codesamplex.dev:443:" + host,
                   "https://codesamplex.dev/" + route]
        result = subprocess.run(command, capture_output=True, timeout=20, check=False)
        if result.returncode:
            raise RuntimeError("public-" + route + "-unavailable")
        if route == "healthz":
            if result.stdout.strip() != b"ok":
                raise RuntimeError("public-healthz-invalid")
            results[route] = "ok"
        else:
            version = validation.parse_json(result.stdout)
            if (version.get("revision") != expected["targetSha"] or
                    version.get("version") != expected["expectedReleaseTag"]):
                raise RuntimeError("public-version-mismatch")
            results[route] = {key: version[key] for key in ("version", "revision")}
    return validation.validate_public_probe(results, expected)


def host_program(expected):
    root = Path(__file__).resolve().parent
    migration = base64.b64encode((root / "offline-migration.py").read_bytes()).decode()
    reconciliation = base64.b64encode((root / "reconcile-committed.py").read_bytes()).decode()
    expectation = base64.b64encode(json.dumps(expected).encode()).decode()
    # Parse the entire bundle before execution; retained host code is not run.
    return ("import base64,sys,types\n"
            "module=types.ModuleType('csx_reconciliation_migration')\n"
            "sys.modules[module.__name__]=module\n"
            f"exec(compile(base64.b64decode('{migration}'),'offline-migration.py','exec'),module.__dict__)\n"
            f"sys.argv=['reconcile-committed.py','--expected-base64','{expectation}']\n"
            f"exec(compile(base64.b64decode('{reconciliation}'),'reconcile-committed.py','exec'),"
            "{'__name__':'__main__','__file__':'reconcile-committed.py'})\n")


def collect_host(args, expected):
    command = ["ssh", "-i", args.key, "-o", "BatchMode=yes",
               "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + args.known_hosts,
               "-o", "ConnectTimeout=5", args.user + "@" + args.host,
               "timeout --signal=TERM --kill-after=5 240 python3 -"]
    result = subprocess.run(command, input=host_program(expected).encode(),
                            capture_output=True, timeout=255, check=False)
    if result.returncode:
        # Remote stdout/stderr can contain application data. Publish only a
        # stage marker with the collector's fixed alphabet.
        stage = "unavailable"
        try:
            failure = validation.parse_json(result.stdout)
            if re.fullmatch(r"[a-z][a-z0-9-]{0,63}", failure.get("failureStage", "")):
                stage = failure["failureStage"]
        except (ValueError, TypeError, AttributeError):
            pass
        raise RuntimeError("host-verification-failed:" + stage)
    report = validation.parse_json(result.stdout)
    validate_host_report(report, expected)
    return report


def validate_host_report(report, expected):
    try:
        return validation.validate_host_report(report, expected)
    except validation.ProvenanceError as error:
        raise RuntimeError("host-report-invalid") from error


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("expected", "provenance", "output", "host", "key", "known-hosts"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--user", default="ubuntu")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-z_][a-z0-9_-]{0,31}", args.user):
        parser.error("invalid SSH user")
    # curl --resolve has IPv6-specific syntax; accept only the configured IPv4 endpoint.
    try:
        ipaddress.IPv4Address(args.host)
    except ValueError:
        parser.error("production host must be an IPv4 address")
    expected = validation.parse_json(Path(args.expected).read_bytes())
    provenance = validation.parse_json(Path(args.provenance).read_bytes())
    evidence = {
        "schemaVersion": 3, "workflowRunId": os.environ["GITHUB_RUN_ID"],
        "operationalSha": os.environ["GITHUB_SHA"],
        "targetSha": expected["targetSha"], "previousProductionSha": expected["previousSha"],
        "trackingIssue": provenance["trackingIssue"], "conclusion": "failure",
        "failureClass": "reconciliation-unresolved", "deployedSha": "", "servedRevision": "unavailable",
        "health": "not-started", "smoke": "not-started", "rollback": "not-attempted",
        "startedAt": utc(), "observation": "pending-independent-workflow",
        "reconciliation": {
            "kind": "committed-host", "originalDeploymentRunId": provenance["originalDeploymentRunId"],
            "originalDeploymentRunNumber": provenance["originalDeploymentRunNumber"],
            "originalOperationalSha": expected["operationalSha"], "owner": expected["owner"],
            "evidenceSha256": expected["evidenceSha256"], "lockDisposition": "retained",
            "originalArtifactId": provenance["originalArtifactId"],
            "originalArtifactSha256": provenance["originalArtifactSha256"],
            "originalEvidenceSha256": provenance["originalEvidenceSha256"],
        },
    }
    status = 1
    try:
        evidence["publicBefore"] = public_probe(args.host, expected)
        evidence["hostReconciliation"] = collect_host(args, expected)
        evidence["publicAfter"] = public_probe(args.host, expected)
        evidence.update(conclusion="success", failureClass="none", deployedSha=expected["targetSha"],
                        servedRevision=expected["targetSha"], imageDigest=expected["imageDigest"],
                        serverStartedAt=expected["serverStartedAt"], migrationVersion=expected["expectedMigration"],
                        health="ok", smoke="pass", rollback="not-needed", acceptanceAuthority="host-reconciled")
        status = 0
    except Exception as error:
        # Stage codes only, never command output or response bodies.
        message = str(error)
        evidence["failure"] = message if re.fullmatch(r"[a-z][a-z0-9:-]{0,100}", message) else "reconciliation-probe-failed"
    finally:
        evidence["completedAt"] = utc()
        Path(args.output).write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    return status


if __name__ == "__main__":
    sys.exit(main())
