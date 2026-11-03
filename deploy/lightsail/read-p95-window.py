#!/usr/bin/env python3
"""Send the fixed collector over the existing pinned SSH identity."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys

SOURCE = Path(__file__).with_name("collect-p95-window.py")
OUTPUT = Path("p95-window.json")


def run(env):
    revision = env.get("EXPECTED_REVISION", "")
    host = env.get("PRODUCTION_HOST", "")
    user = env.get("PRODUCTION_USER", "") or "ubuntu"
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        raise ValueError("invalid_revision")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]{0,252}", host) or not re.fullmatch(r"[a-z_][a-z0-9_-]{0,31}", user):
        raise ValueError("invalid_host")
    key = Path(env["PRODUCTION_KEY_PATH"]).resolve(strict=True)
    known = Path(env["PRODUCTION_KNOWN_HOSTS_PATH"]).resolve(strict=True)
    command = ("ssh", "-F", os.devnull, "-T", "-i", str(key), "-o", "BatchMode=yes",
               "-o", "IdentitiesOnly=yes", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no",
               "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + str(known),
               "-o", "ConnectTimeout=5", user + "@" + host,
               "EXPECTED_REVISION=" + revision + " timeout --kill-after=2s 360s python3 -I -B -")
    result = subprocess.run(command, input=SOURCE.read_bytes(), capture_output=True, timeout=370)
    if result.returncode or len(result.stdout) > 131072:
        raise RuntimeError("remote_collection_failed")
    data = json.loads(result.stdout)
    if data.get("schema") != 1 or data.get("rounds") != 20 or len(data.get("samples", [])) != 60 or len(data.get("postgres", [])) != 20:
        raise RuntimeError("invalid_evidence")
    OUTPUT.write_text(json.dumps({"expectedRevision": revision, "diagnostic": data}, separators=(",", ":")) + "\n", encoding="utf-8")


if __name__ == "__main__":
    try:
        run(os.environ)
    except Exception as exc:
        print("p95 collection unavailable: " + str(exc) if isinstance(exc, (ValueError, RuntimeError)) else "p95 collection unavailable", file=sys.stderr)
        raise SystemExit(1)
