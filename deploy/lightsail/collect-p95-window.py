#!/usr/bin/env python3
"""Bounded, read-only, same-host p95 diagnosis. Prints aggregate JSON only."""
import http.client
import json
import math
import os
import socket
import ssl
import subprocess
import time
from datetime import datetime, timezone

PATHS = ("/v1/stats", "/healthz", "/v1/shards/npm/zod/3")
ROUNDS = 20
SQL = """SELECT json_build_object(
 'at',clock_timestamp(),
 'active',(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND pid<>pg_backend_pid()),
 'waits',(SELECT coalesce(json_agg(row_to_json(w)),'[]'::json) FROM
   (SELECT coalesce(wait_event_type,'running') AS class,count(*) AS count
    FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND pid<>pg_backend_pid()
    GROUP BY 1 ORDER BY 1) w),
 'blocked',(SELECT count(*) FROM pg_locks WHERE NOT granted),
 'slow',(SELECT coalesce(json_agg(row_to_json(s)),'[]'::json) FROM
   (SELECT queryid,calls,round(max_exec_time::numeric,1) AS max_ms,
           round(mean_exec_time::numeric,1) AS mean_ms
    FROM public.pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database())
    ORDER BY max_exec_time DESC LIMIT 5) s))"""
DB_COMMAND = ("docker", "compose", "-f", "/opt/codesamplex/deploy/docker-compose.yml",
              "exec", "-T", "-e", "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=1500 -c lock_timeout=500 -c jit=off",
              "-e", "PGCONNECT_TIMEOUT=2", "db", "psql", "-X", "-v", "ON_ERROR_STOP=1",
              "-U", "csx", "-d", "csx", "-Atqc", SQL)


def utc():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds")


def cpu():
    values = open("/proc/stat", encoding="ascii").readline().split()
    assert values[0] == "cpu" and len(values) >= 9
    ticks = [int(x) for x in values[1:9]]
    return sum(ticks), ticks[7]


def database():
    result = subprocess.run(DB_COMMAND, capture_output=True, timeout=4)
    if result.returncode or len(result.stdout) > 8192:
        return {"available": False, "reason": "db_command_failed"}
    try:
        return {"available": True, "data": json.loads(result.stdout)}
    except (ValueError, UnicodeError):
        return {"available": False, "reason": "invalid_db_response"}


def request(path):
    start = time.monotonic()
    conn = http.client.HTTPSConnection("codesamplex.dev", timeout=6)
    try:
        raw = socket.create_connection(("127.0.0.1", 443), timeout=6)
        conn.sock = ssl.create_default_context().wrap_socket(raw, server_hostname="codesamplex.dev")
        sent = time.monotonic()
        conn.request("GET", path, headers={"Host": "codesamplex.dev", "User-Agent": "csx-perf-slo-monitor/1 (+#537)"})
        response = conn.getresponse()
        first = time.monotonic()
        header = response.getheader("Server-Timing", "")
        status = response.status
        # Discard a bounded response. TTFB ends at headers; body is not evidence.
        response.read(1048576)
        phases = {}
        for item in header.split(","):
            parts = item.strip().split(";dur=")
            if len(parts) == 2 and parts[0] in ("middleware", "db_wait", "query_handler", "serialize"):
                phases[parts[0]] = float(parts[1])
        return {"status": status, "ttfbMs": round((first-sent)*1000, 2),
                "phasesMs": phases, "at": utc(), "elapsedMs": round((first-start)*1000, 2)}
    except (OSError, ValueError, http.client.HTTPException) as exc:
        return {"status": None, "error": type(exc).__name__, "at": utc()}
    finally:
        conn.close()


def percentile(values):
    if not values:
        return None
    return round(sorted(values)[math.ceil(0.95 * len(values))-1], 2)


def collect():
    expected = os.environ.get("EXPECTED_REVISION", "")
    identity = subprocess.run(("docker", "inspect", "--format", "{{ index .Config.Labels \"org.opencontainers.image.revision\" }}", "codesamplex-server-1"), capture_output=True, timeout=4)
    if len(expected) != 40 or identity.returncode or identity.stdout.decode("ascii", "replace").strip() != expected:
        raise RuntimeError("revision_mismatch")
    started = utc()
    samples = []
    db = []
    previous = cpu()
    for round_id in range(ROUNDS):
        for path in PATHS:
            before = cpu()
            response = request(path)
            after = cpu()
            total = after[0]-before[0]
            response.update(path=path, round=round_id+1,
                            cpuStealPercent=round(100*(after[1]-before[1])/total, 2) if total > 0 else None)
            samples.append(response)
            time.sleep(0.25)
        db.append(database())
        time.sleep(0.25)
    final = cpu()
    total = final[0]-previous[0]
    summaries = {}
    for path in PATHS:
        group = [s for s in samples if s["path"] == path]
        ok = [s for s in group if s.get("status") == 200]
        phase_names = ("middleware", "db_wait", "query_handler", "serialize")
        summaries[path] = {"count": len(group), "success": len(ok),
                           "ttfbP95Ms": percentile([s["ttfbMs"] for s in ok]),
                           "appPhaseP95Ms": {name: percentile([s["phasesMs"][name] for s in ok if name in s["phasesMs"]]) for name in phase_names},
                           "caddyForwardedUpstreamAppP95Ms": percentile([sum(s["phasesMs"].values()) for s in ok if len(s["phasesMs"]) == 4]),
                           "caddyResidualP95Ms": percentile([max(0, s["ttfbMs"]-sum(s["phasesMs"].values())) for s in ok if len(s["phasesMs"]) == 4])}
    return {"schema": 1, "startedAt": started, "finishedAt": utc(), "rounds": ROUNDS,
            "hostStealPercentWindow": round(100*(final[1]-previous[1])/total, 2) if total > 0 else None,
            "routes": summaries, "samples": samples, "postgres": db,
            "caddyTimingNote": "Caddy forwards Server-Timing; residual is local TLS/request/proxy/transfer overhead, not an exact upstream dial timer"}


if __name__ == "__main__":
    print(json.dumps(collect(), separators=(",", ":")))
