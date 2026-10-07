#!/usr/bin/env python3
"""Bounded, read-only five-route SLO and host sampler for issue #531.

Run on the production host through an existing pinned SSH connection. Each
route is sampled at most once every five seconds; output is JSONL, one UTC
minute window per line, so a disconnected session retains complete windows.
"""

import concurrent.futures
import datetime
import http.client
import json
import math
import socket
import ssl
import time


PATHS = {
    "healthz": ("/healthz", 0.3731),
    "version": ("/version", 0.3311),
    "stats": ("/v1/stats", 0.3425),
    "shard": ("/v1/shards/npm/zod/3", 0.4155),
    "verification_jobs": (
        "/v1/verification/jobs?peerId=ed25519:0123456789abcdef&limit=10", 0.5771
    ),
}
WINDOWS = 9
SECONDS_PER_WINDOW = 60
ROUND_SPACING = 5
TIMEOUT = 10
CONTEXT = ssl._create_unverified_context()


def utc_now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def cpu():
    with open("/proc/stat", encoding="ascii") as stream:
        fields = stream.readline().split()[1:9]
    return tuple(map(int, fields))


def load():
    with open("/proc/loadavg", encoding="ascii") as stream:
        return float(stream.read().split()[0])


def probe(path):
    conn = http.client.HTTPSConnection("127.0.0.1", timeout=TIMEOUT, context=CONTEXT)
    try:
        t0 = time.perf_counter()
        sock = socket.create_connection(("127.0.0.1", 443), timeout=TIMEOUT)
        t1 = time.perf_counter()
        sock = CONTEXT.wrap_socket(sock, server_hostname="codesamplex.dev")
        t2 = time.perf_counter()
        conn.sock = sock
        conn.request("GET", path, headers={
            "Host": "codesamplex.dev",
            "Accept": "application/json",
            "User-Agent": "csx-perf-slo-monitor/1 (+#531)",
        })
        response = conn.getresponse()
        t3 = time.perf_counter()
        response.read()
        status = response.status
        return {"status": status, "serverSeconds": round(max(0, (t3 - t2) - (t1 - t0)), 4),
                "ttfbSeconds": round(t3 - t0, 4)}
    except (OSError, http.client.HTTPException) as exc:
        return {"status": None, "serverSeconds": TIMEOUT, "error": type(exc).__name__}
    finally:
        conn.close()


def percentile95(values):
    return sorted(values)[math.ceil(len(values) * .95) - 1] if values else None


def main():
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(PATHS)) as pool:
        for window in range(WINDOWS):
            started = time.monotonic()
            stop = started + SECONDS_PER_WINDOW
            at = utc_now()
            before = cpu()
            loads = [load()]
            samples = {name: [] for name in PATHS}
            next_round = started
            while time.monotonic() < stop:
                futures = {name: pool.submit(probe, path) for name, (path, _) in PATHS.items()}
                for name, future in futures.items():
                    samples[name].append(future.result())
                loads.append(load())
                next_round = max(next_round + ROUND_SPACING, time.monotonic() + 0.01)
                time.sleep(max(0, min(next_round, stop) - time.monotonic()))
            after = cpu()
            delta = [y - x for x, y in zip(before, after)]
            total = sum(delta)
            active = sum(delta[i] for i in (0, 1, 2, 5, 6))
            route = {}
            for name, (_, target) in PATHS.items():
                rows = samples[name]
                # Match scripts/perf-slo.py: non-2xx and transport failures
                # consume the full timeout budget in the p95 calculation.
                p95 = percentile95([
                    r["serverSeconds"] if r["status"] is not None and 200 <= r["status"] < 300
                    else TIMEOUT for r in rows
                ])
                route[name] = {"count": len(rows), "p95Seconds": p95,
                              "targetSeconds": target, "violated": p95 > target,
                              "errors": sum(r["status"] is None or not 200 <= r["status"] < 300
                                            for r in rows),
                              "statuses": [r["status"] for r in rows], "samples": rows}
            print(json.dumps({"window": window + 1, "startUTC": at, "endUTC": utc_now(),
                              "stealPercent": round(100 * delta[7] / total, 2),
                              "cpuActivePercent": round(100 * active / total, 2),
                              "load1Mean": round(sum(loads) / len(loads), 2),
                              "runtime": "unavailable on served v0.2.4",
                              "routes": route}, separators=(",", ":")), flush=True)


if __name__ == "__main__":
    main()
