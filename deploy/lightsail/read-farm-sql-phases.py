#!/usr/bin/env python3
"""Reviewed stdin-only transport; fixed unavailable artifact survives failures."""
import importlib.util
import json
import os
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parent


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


transport = load("_farm_bounded_transport", "collect-authoring-funnel.py")
phases = load("farm_sql_phases", "collect-farm-sql-phases.py")
REMOTE_COMMAND = "timeout --kill-after=2s 108s python3 -I -B -"


def remote_source():
    # Reuse PR372's bounded pipe/identity helpers without running its collector.
    # Both reviewed files exist only in interpreter memory on the remote host.
    helper = (ROOT / "collect-authoring-funnel.py").read_text(encoding="utf-8")
    collector = (ROOT / "collect-farm-sql-phases.py").read_text(encoding="utf-8")
    source = ("import sys, types\n"
              "shared = types.ModuleType('_farm_bounded_transport')\n"
              "sys.modules[shared.__name__] = shared\n"
              "exec(compile(" + repr(helper) + ", '<bounded-transport>', 'exec'), shared.__dict__)\n"
              "exec(compile(" + repr(collector) + ", '<farm-sql-phases>', 'exec'))\n").encode("utf-8")
    phases.require(len(source) <= 65536)
    return source


def remote(env, run=transport.bounded_command):
    expected = env.get("EXPECTED_REVISION", "")
    phases.require(expected == phases.CATALOG_REVISION)
    host, user = env.get("PRODUCTION_HOST", ""), env.get("PRODUCTION_USER", "") or "ubuntu"
    phases.require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]{0,252}", host) is not None)
    phases.require(re.fullmatch(r"[a-z_][a-z0-9_-]{0,31}", user) is not None)
    key, known = (Path(env[name]).resolve() for name in ("PRODUCTION_KEY_PATH", "PRODUCTION_KNOWN_HOSTS_PATH"))
    phases.require(key.is_file() and known.is_file())
    command = ("ssh", "-F", "/dev/null", "-T", "-i", str(key), "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
               "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "StrictHostKeyChecking=yes",
               "-o", "UserKnownHostsFile=" + str(known), "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=5",
               "-o", "ServerAliveCountMax=2", user + "@" + host, REMOTE_COMMAND + " " + expected)
    return run(command, 115, phases.MAX_SUMMARY_BYTES, source=remote_source())


def diagnose(env, call=remote):
    expected = env.get("EXPECTED_REVISION", "")
    if not transport.valid_revision(expected):
        return phases.empty("invalid_expected_revision")
    if expected != phases.CATALOG_REVISION:
        return phases.empty("unsupported_catalog", expected)
    try:
        raw = call(env)
    except Exception:
        return phases.empty("transport_failed", expected)
    try:
        return phases.validate(raw, expected)
    except Exception:
        return phases.empty("invalid_summary", expected)


def main(initialize=False):
    sha, run_id = os.environ.get("GITHUB_SHA", ""), os.environ.get("GITHUB_RUN_ID", "")
    phases.require(transport.valid_revision(sha) and re.fullmatch(r"[1-9][0-9]{0,19}", run_id) is not None)
    expected = os.environ.get("EXPECTED_REVISION", "")
    if not transport.valid_revision(expected):
        result = phases.empty("invalid_expected_revision")
    elif expected != phases.CATALOG_REVISION:
        result = phases.empty("unsupported_catalog", expected)
    else:
        result = phases.empty(expected=expected) if initialize else diagnose(os.environ)
    Path("farm-sql-phases.json").write_text(json.dumps({"operationalSha": sha, "workflowRunId": int(run_id),
        "diagnostic": result}, separators=(",", ":"), ensure_ascii=True) + "\n", encoding="utf-8")
    return 0 if (initialize and result["failureClass"] == "not_collected") or result["availability"] == "available" else 1


if __name__ == "__main__":
    try:
        phases.require(sys.argv[1:] in ([], ["--initialize"]))
        code = main(sys.argv[1:] == ["--initialize"])
    except Exception:
        code = 1
    raise SystemExit(code)
