#!/usr/bin/env python3
"""Send only reviewed fixed code; retain only validated scalar evidence."""
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
throughput = load("farm_throughput", "collect-farm-throughput.py")
throughput.SQL_TEMPLATE = (ROOT / "farm-throughput.sql").read_text(encoding="utf-8")
REMOTE_COMMAND = "timeout --kill-after=2s 15s python3 -I -B -"


def inputs(env):
    return (env.get("EXPECTED_REVISION", ""), env.get("VERIFIER_PEER", ""),
            tuple(env.get("SLOT%d_LABEL_SHA256" % n, "") for n in (1, 2, 3)))


def remote_source():
    helper = (ROOT / "collect-authoring-funnel.py").read_text(encoding="utf-8")
    collector = (ROOT / "collect-farm-throughput.py").read_text(encoding="utf-8")
    source = ("import sys, types\n"
              "shared = types.ModuleType('_farm_bounded_transport')\n"
              "sys.modules[shared.__name__] = shared\n"
              "exec(compile(" + repr(helper) + ", '<bounded-transport>', 'exec'), shared.__dict__)\n"
              "SQL_TEMPLATE_FROM_TRANSPORT = " + repr(throughput.SQL_TEMPLATE) + "\n"
              "exec(compile(" + repr(collector) + ", '<farm-throughput>', 'exec'))\n").encode("utf-8")
    throughput.require(len(source) <= 65536)
    return source


def remote(env, run=transport.bounded_command):
    expected, peer, slots = inputs(env)
    throughput.require(transport.valid_revision(expected))
    throughput.binding(peer, slots)
    host, user = env.get("PRODUCTION_HOST", ""), env.get("PRODUCTION_USER", "") or "ubuntu"
    throughput.require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]{0,252}", host) is not None)
    throughput.require(re.fullmatch(r"[a-z_][a-z0-9_-]{0,31}", user) is not None)
    key, known = (Path(env[name]).resolve() for name in ("PRODUCTION_KEY_PATH", "PRODUCTION_KNOWN_HOSTS_PATH"))
    throughput.require(key.is_file() and known.is_file())
    command = ("ssh", "-F", "/dev/null", "-T", "-i", str(key), "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
               "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "StrictHostKeyChecking=yes",
               "-o", "UserKnownHostsFile=" + str(known), "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=5",
               "-o", "ServerAliveCountMax=2", user + "@" + host,
               REMOTE_COMMAND + " " + " ".join((expected, peer) + slots))
    return run(command, 22, throughput.MAX_BYTES, source=remote_source())


def diagnose(env, call=remote, initialize=False):
    expected, peer, slots = inputs(env)
    if not transport.valid_revision(expected):
        return throughput.empty(expected, peer, slots, "invalid_expected_revision", "validation")
    try:
        throughput.binding(peer, slots)
    except Exception:
        return throughput.empty(expected, peer, slots, "invalid_binding", "validation")
    if initialize:
        return throughput.empty(expected, peer, slots)
    try:
        raw = call(env)
    except Exception:
        return throughput.empty(expected, peer, slots, "transport_failed")
    try:
        return throughput.validate(raw, expected, peer, slots)
    except Exception:
        return throughput.empty(expected, peer, slots, "invalid_summary")


def main(initialize=False):
    sha, run_id = os.environ.get("GITHUB_SHA", ""), os.environ.get("GITHUB_RUN_ID", "")
    throughput.require(transport.valid_revision(sha) and re.fullmatch(r"[1-9][0-9]{0,19}", run_id) is not None)
    result = diagnose(os.environ, initialize=initialize)
    Path("farm-throughput.json").write_text(json.dumps({"operationalSha": sha, "workflowRunId": int(run_id),
        "diagnostic": result}, separators=(",", ":"), ensure_ascii=True) + "\n", encoding="utf-8")
    return 0 if (initialize and result["failureClass"] == "not_collected") or result["availability"] == "available" else 1


if __name__ == "__main__":
    try:
        throughput.require(sys.argv[1:] in ([], ["--initialize"]))
        code = main(initialize=sys.argv[1:] == ["--initialize"])
    except Exception:
        code = 1
    raise SystemExit(code)
