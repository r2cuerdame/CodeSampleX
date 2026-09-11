#!/usr/bin/env python3
"""Transport a reviewed stdin-only collector and publish only validated aggregates."""
import importlib.util
import json
import os
from pathlib import Path
import re
import sys
import time

ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("authoring_funnel", ROOT / "collect-authoring-funnel.py")
funnel = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(funnel)
REMOTE_COMMAND = "timeout --kill-after=2s 25s python3 -I -B -"


def require(ok):
    if not ok:
        raise ValueError("invalid bounded evidence")


def keys(value, expected):
    require(type(value) is dict and set(value) == set(expected))


def integer(value, minimum, maximum):
    require(type(value) is int and minimum <= value <= maximum)


def unique_object(pairs):
    out = {}
    for key, value in pairs:
        require(key not in out)
        out[key] = value
    return out


def validate(raw, expected_revision=None):
    require(len(raw) <= 16384)
    value = json.loads(raw.decode("utf-8", errors="strict"), object_pairs_hook=unique_object)
    template = funnel.empty_summary(time.time_ns())
    keys(value, template)
    for key in ("schemaVersion", "scope", "windowSeconds", "windowCoverage", "funnelScope", "fallbackScope", "eligibilityScope", "snapshotAgeScope"):
        require(type(value[key]) is type(template[key]) and value[key] == template[key])
    start, end = (funnel.timestamp_ns(value[k]) for k in ("windowStart", "windowEnd"))
    require(end - start == funnel.WINDOW_SECONDS * 1000000000)
    require(value["availability"] in ("available", "unavailable") and value["failureClass"] in funnel.FAILURES)
    if value["failureClass"] == "invalid_expected_revision":
        require(value["expectedRevision"] is None)
    else:
        require(funnel.valid_revision(value["expectedRevision"]))
    if expected_revision is not None:
        require(value["expectedRevision"] == expected_revision)
    if value["availability"] == "unavailable":
        require(value["failureClass"] != "none")
        require(all(value[k] is None for k in ("identity", "read", "funnel", "fallback")))
        return value
    require(value["failureClass"] == "none")
    identity = value["identity"]
    keys(identity, ("imageDigest", "revision", "startedAt"))
    require(type(identity["imageDigest"]) is str and re.fullmatch(r"sha256:[0-9a-f]{64}", identity["imageDigest"]) is not None)
    require(type(identity["revision"]) is str and re.fullmatch(r"[0-9a-f]{40}", identity["revision"]) is not None)
    require(identity["revision"] == value["expectedRevision"])
    require(funnel.timestamp_ns(identity["startedAt"]) <= end)
    read = value["read"]
    keys(read, ("lines", "bytes", "firstAt", "lastAt"))
    integer(read["lines"], 0, funnel.MAX_LINES)
    integer(read["bytes"], 0, funnel.MAX_BYTES)
    if read["lines"] == 0:
        require(read["bytes"] == 0 and read["firstAt"] is None and read["lastAt"] is None)
    else:
        require(start <= funnel.timestamp_ns(read["firstAt"]) <= funnel.timestamp_ns(read["lastAt"]) <= end)
    polls = value["funnel"]
    keys(polls, ("polls", "wantedReadSum", "wantedEligibleSum", "expansionReadSum", "expansionEligibleSum",
                 "snapshotAgeNsMin", "snapshotAgeNsMax", "servedCounts", "lastPoll"))
    integer(polls["polls"], 0, read["lines"])
    for prefix, limit in (("wanted", 200), ("expansion", 400)):
        integer(polls[prefix + "ReadSum"], 0, polls["polls"] * limit)
        integer(polls[prefix + "EligibleSum"], 0, polls[prefix + "ReadSum"])
    keys(polls["servedCounts"], funnel.KINDS)
    for count in polls["servedCounts"].values():
        integer(count, 0, polls["polls"])
    require(sum(polls["servedCounts"].values()) == polls["polls"])
    if polls["polls"] == 0:
        require(all(polls[k] is None for k in ("lastPoll", "snapshotAgeNsMin", "snapshotAgeNsMax")))
    else:
        integer(polls["snapshotAgeNsMin"], -funnel.MAX_AGE_NS, funnel.MAX_AGE_NS)
        integer(polls["snapshotAgeNsMax"], polls["snapshotAgeNsMin"], funnel.MAX_AGE_NS)
        last = polls["lastPoll"]
        keys(last, ("at", "wantedRead", "wantedEligible", "expansionRead", "expansionEligible", "served", "snapshotAgeNs"))
        require(start <= funnel.timestamp_ns(last["at"]) <= end and last["served"] in funnel.KINDS)
        for prefix, limit in (("wanted", 200), ("expansion", 400)):
            integer(last[prefix + "Read"], 0, limit)
            integer(last[prefix + "Eligible"], 0, last[prefix + "Read"])
        integer(last["snapshotAgeNs"], polls["snapshotAgeNsMin"], polls["snapshotAgeNsMax"])
    fallback = value["fallback"]
    keys(fallback, ("events", "byClass"))
    integer(fallback["events"], 0, read["lines"] - polls["polls"])
    keys(fallback["byClass"], funnel.ERROR_CLASSES)
    for count in fallback["byClass"].values():
        integer(count, 0, fallback["events"])
    require(sum(fallback["byClass"].values()) == fallback["events"])
    return value


def remote(env, run=funnel.bounded_command):
    expected_revision = env.get("EXPECTED_REVISION", "")
    require(funnel.valid_revision(expected_revision))
    host, user = env.get("PRODUCTION_HOST", ""), env.get("PRODUCTION_USER", "") or "ubuntu"
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]{0,252}", host) is not None)
    require(re.fullmatch(r"[a-z_][a-z0-9_-]{0,31}", user) is not None)
    key, known_hosts = (Path(env[name]).resolve() for name in ("PRODUCTION_KEY_PATH", "PRODUCTION_KNOWN_HOSTS_PATH"))
    require(key.is_file() and known_hosts.is_file())
    source = (ROOT / "collect-authoring-funnel.py").read_bytes()
    require(len(source) <= 65536)
    command = ("ssh", "-T", "-i", str(key), "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
               "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + str(known_hosts),
               "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=2",
               user + "@" + host, REMOTE_COMMAND + " " + expected_revision)
    return run(command, 35, 16384, source=source)


def diagnose(env, transport=remote):
    expected_revision = env.get("EXPECTED_REVISION", "")
    if not funnel.valid_revision(expected_revision):
        return funnel.empty_summary(time.time_ns(), "invalid_expected_revision")
    result = funnel.empty_summary(time.time_ns(), "transport_failed", expected_revision)
    try:
        raw = transport(env)
    except Exception:
        return result
    try:
        return validate(raw, expected_revision)
    except Exception:
        result["failureClass"] = "invalid_summary"
        return result


def main(initialize=False):
    sha, run_id = os.environ.get("GITHUB_SHA", ""), os.environ.get("GITHUB_RUN_ID", "")
    require(re.fullmatch(r"[0-9a-f]{40}", sha) is not None and re.fullmatch(r"[1-9][0-9]{0,19}", run_id) is not None)
    # Preparation runs before CI/SSH setup and reads only workflow identity
    # and the expected revision, never production credentials.
    # It must retain the same envelope without entering any remote path.
    expected_revision = os.environ.get("EXPECTED_REVISION", "")
    if not funnel.valid_revision(expected_revision):
        result = funnel.empty_summary(time.time_ns(), "invalid_expected_revision")
    else:
        result = funnel.empty_summary(time.time_ns(), expected_revision=expected_revision) if initialize else diagnose(os.environ)
    evidence = {"operationalSha": sha, "workflowRunId": int(run_id), "diagnostic": result}
    Path("authoring-funnel.json").write_text(json.dumps(evidence, separators=(",", ":"), ensure_ascii=True) + "\n", encoding="utf-8")
    return 0 if (initialize and result["failureClass"] == "not_collected") or result["availability"] == "available" else 1


if __name__ == "__main__":
    try:
        require(sys.argv[1:] in ([], ["--initialize"]))
        exit_code = main(initialize=sys.argv[1:] == ["--initialize"])
    except Exception:
        # Never print exception arguments, SSH output, or a failed JSON body.
        exit_code = 1
    raise SystemExit(exit_code)
