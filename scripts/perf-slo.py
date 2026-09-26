#!/usr/bin/env python3
"""Performance SLO probe for the public CodeSampleX paths (#511).

Three subcommands, each small on purpose:

  measure    GET each configured path N times against production and write
             median/p95 per path as JSON. Read-only GETs, one at a time, never
             an authoring or verification claim: a single-node Farm makes a
             heavy monitor itself the slowdown (#149).
  baseline   Turn one measure result into the stored targets
             (scripts/perf-slo-baseline.json).
  reconcile  Compare this run with the previous one and open, update or close
             exactly one GitHub Issue per path. Two consecutive violations
             open it; two consecutive passes close it.

Every request opens a fresh connection and is split into its phases: TCP
connect, TLS handshake, and request-to-first-byte. Time to first byte (the
number #485 took with curl -w) is recorded, but the SLO is on server time:
request-to-first-byte minus one round trip, the round trip being the TCP
connect. A GitHub runner's distance to the server changes from run to run
(/healthz TTFB p95 moved 0.68 s -> 0.93 s in ten minutes while the server did
not), and a raw-TTFB target would page on the runner, not the service.

A request that fails (non-2xx, timeout, connection error) counts as a sample
at the timeout, so a path that fails more than one request in twenty violates
its p95 however fast the failures were.

Standard library only; the workflow runs it with `python3 -I -B`.
"""

import argparse
import datetime
import http.client
import json
import math
import os
import socket
import ssl
import subprocess
import sys
import time
import urllib.parse

SCHEMA_VERSION = 1
MARKER_PREFIX = "<!-- perf-slo path="


# ---------------------------------------------------------------------------
# Statistics


def median(values):
    ordered = sorted(values)
    n = len(ordered)
    if n == 0:
        return None
    mid = n // 2
    if n % 2:
        return ordered[mid]
    return (ordered[mid - 1] + ordered[mid]) / 2


def p95(values):
    """Nearest-rank 95th percentile: the 19th of 20 samples."""
    ordered = sorted(values)
    if not ordered:
        return None
    rank = math.ceil(0.95 * len(ordered))
    return ordered[rank - 1]


def rnd(value):
    return None if value is None else round(value, 4)


def summarize(samples, timeout):
    """samples: list of {"ok", "status", "server", "ttfb", "connect"}.

    medianSeconds/p95Seconds are server time, the SLO metric. ttfb* are the
    raw client-side numbers, kept for comparison with measurements such as
    #485's; rttMedianSeconds is the TCP connect, i.e. the round trip that was
    subtracted.
    """
    server = [s["server"] if s["ok"] else float(timeout) for s in samples]
    ttfb = [s["ttfb"] if s["ok"] else float(timeout) for s in samples]
    rtt = [s["connect"] for s in samples if s.get("connect") is not None]
    return {
        "count": len(samples),
        "errors": sum(1 for s in samples if not s["ok"]),
        "medianSeconds": rnd(median(server)),
        "p95Seconds": rnd(p95(server)),
        "ttfbMedianSeconds": rnd(median(ttfb)),
        "ttfbP95Seconds": rnd(p95(ttfb)),
        "rttMedianSeconds": rnd(median(rtt)),
        "statuses": sorted({str(s.get("status")) for s in samples}),
    }


# ---------------------------------------------------------------------------
# Measurement

# "monitor" is a likelyAutomated marker in internal/activity, so probe traffic
# is never recorded as production network activity. The activity tests read
# this line to prove it for every configured path; keep it a plain literal.
USER_AGENT = "csx-perf-slo-monitor/1 (+#511)"


def probe_once(base_url, path, timeout):
    parsed = urllib.parse.urlsplit(base_url)
    https = parsed.scheme == "https"
    host = parsed.hostname
    port = parsed.port or (443 if https else 80)
    conn_cls = http.client.HTTPSConnection if https else http.client.HTTPConnection
    conn = conn_cls(parsed.netloc, timeout=timeout)
    try:
        t0 = time.perf_counter()
        sock = socket.create_connection((host, port), timeout=timeout)
        t1 = time.perf_counter()
        if https:
            sock = ssl.create_default_context().wrap_socket(sock, server_hostname=host)
        t2 = time.perf_counter()
        conn.sock = sock  # already connected: http.client will not reconnect
        conn.request("GET", path, headers={"User-Agent": USER_AGENT,"Accept": "application/json"})
        resp = conn.getresponse()
        t3 = time.perf_counter()
        resp.read()
        return {
            "ok": 200 <= resp.status < 300,
            "status": resp.status,
            "ttfb": round(t3 - t0, 4),
            "connect": round(t1 - t0, 4),
            "tls": round(t2 - t1, 4),
            # Request to first byte is one round trip plus the server's work;
            # the TCP connect is one round trip.
            "server": round(max(0.0, (t3 - t2) - (t1 - t0)), 4),
        }
    except (OSError, http.client.HTTPException) as exc:
        return {"ok": False, "status": None, "ttfb": None, "server": None, "error": type(exc).__name__}
    finally:
        conn.close()


def read_version(base_url, timeout):
    parsed = urllib.parse.urlsplit(base_url)
    conn_cls = http.client.HTTPSConnection if parsed.scheme == "https" else http.client.HTTPConnection
    conn = conn_cls(parsed.netloc, timeout=timeout)
    try:
        conn.request("GET", "/version", headers={"Accept": "application/json"})
        resp = conn.getresponse()
        body = json.loads(resp.read().decode("utf-8"))
        return {"version": body.get("version"), "revision": body.get("revision")}
    except (OSError, http.client.HTTPException, ValueError):
        return {"version": None, "revision": None}
    finally:
        conn.close()


def cmd_measure(args):
    config = load_json(args.config)
    base_url = args.base_url or config["baseUrl"]
    rounds = args.rounds or config["rounds"]
    timeout = config.get("timeoutSeconds", 10)
    spacing = config.get("spacingSeconds", 0.5)
    before = read_version(base_url, timeout)
    paths = []
    # Round-robin rather than 20-in-a-row per path, so a short burst of
    # pressure lands on every path a little instead of on one path entirely.
    samples = {p["name"]: [] for p in config["paths"]}
    for _ in range(rounds):
        for p in config["paths"]:
            samples[p["name"]].append(probe_once(base_url, p["path"], timeout))
            time.sleep(spacing)
    after = read_version(base_url, timeout)
    for p in config["paths"]:
        entry = {"name": p["name"], "path": p["path"]}
        entry.update(summarize(samples[p["name"]], timeout))
        entry["samples"] = [s["server"] for s in samples[p["name"]]]
        target = p.get("targetSeconds")
        entry["targetSeconds"] = target
        entry["violated"] = target is not None and entry["p95Seconds"] > target
        paths.append(entry)
    result = {
        "schemaVersion": SCHEMA_VERSION,
        "measuredAt": utc_now(),
        "baseUrl": base_url,
        "metric": "server_seconds",
        "rounds": rounds,
        "vantage": args.vantage,
        "run": os.environ.get("GITHUB_RUN_ID"),
        "server": after if after == before else {"before": before, "after": after, "changed": True},
        "paths": paths,
    }
    write_json(args.out, result)
    for e in paths:
        print("%-18s server median=%.3fs p95=%.3fs target=%s | ttfb p95=%.3fs rtt=%.3fs errors=%d %s" % (
            e["name"], e["medianSeconds"], e["p95Seconds"],
            "-" if e["targetSeconds"] is None else "%.3fs" % e["targetSeconds"],
            e["ttfbP95Seconds"], e["rttMedianSeconds"] or 0, e["errors"],
            "VIOLATION" if e["violated"] else "ok"))
    return 0


# ---------------------------------------------------------------------------
# Baseline


def cmd_baseline(args):
    """Rewrite the config's targets from one measure result.

    Target = baseline p95, unless the path already violates its last
    known-good p95; then the known-good p95 is the target. Known-good numbers
    were measured from a particular vantage, so the comparison uses a result
    from that same vantage (--same-vantage-result) when one is given.
    """
    config = load_json(args.config)
    result = load_json(args.result)
    same = load_json(args.same_vantage_result) if args.same_vantage_result else None
    measured = {e["name"]: e for e in result["paths"]}
    same_by = {e["name"]: e for e in same["paths"]} if same else {}
    for p in config["paths"]:
        m = measured[p["name"]]
        p["baselineMedianSeconds"] = m["medianSeconds"]
        p["baselineP95Seconds"] = m["p95Seconds"]
        p["baselineTtfbP95Seconds"] = m["ttfbP95Seconds"]
        p["targetSeconds"], p["targetSource"] = choose_target(m, p.get("knownGood"), same_by.get(p["name"]))
    server = result.get("server", {})
    config["baseline"] = {
        "measuredAt": result["measuredAt"],
        "version": server.get("version"),
        "revision": server.get("revision"),
        "vantage": result.get("vantage"),
        "run": result.get("run"),
        "rounds": result["rounds"],
    }
    if same:
        config["baseline"]["knownGoodComparison"] = {
            "measuredAt": same["measuredAt"],
            "vantage": same.get("vantage"),
            "ttfbP95Seconds": {e["name"]: e["ttfbP95Seconds"] for e in same["paths"]},
        }
    write_json(args.config, config)
    return 0


def choose_target(measured, known_good, same_vantage):
    """Known-good numbers are raw TTFB (#485 used curl -w), so the violation
    test compares TTFB with TTFB from the same vantage. When the path violates
    it, the target is the known-good TTFB less that vantage's median network
    share (TTFB minus server time), which puts it on the server-time scale."""
    baseline_p95 = measured["p95Seconds"]
    if not known_good:
        return baseline_p95, "baseline-p95"
    ref = same_vantage or measured
    if ref["ttfbP95Seconds"] > known_good["ttfbP95Seconds"]:
        network = ref["ttfbMedianSeconds"] - ref["medianSeconds"]
        return rnd(max(0.0, known_good["ttfbP95Seconds"] - network)), "known-good-p95"
    return baseline_p95, "baseline-p95"


# ---------------------------------------------------------------------------
# Decision (pure; covered by perf_slo_test.py)


def decide(current, previous, open_issue):
    """One path's action for this run.

    current, previous: the path's entry from a measure result (previous may be
    None: first run, expired artifact, or a newly added path). open_issue: the
    open violation Issue number for this path, or None.

    Returns (action, reason) with action one of:
      "open"    two consecutive violations and no Issue yet
      "update"  an Issue is open; attach this run's measurements
      "close"   an Issue is open and the SLO held on this run and the previous
      "none"    nothing to do
    """
    now_bad = bool(current["violated"])
    prev_bad = None if previous is None else bool(previous["violated"])
    if open_issue is None:
        if now_bad and prev_bad:
            return "open", "p95 above target on two consecutive runs"
        return "none", "no open Issue and no second consecutive violation"
    if not now_bad and prev_bad is False:
        return "close", "SLO held on two consecutive runs"
    if now_bad:
        return "update", "still violating"
    return "update", "held once; one more passing run closes it"


def plan(result, previous_result, open_issues):
    """All actions for one run. open_issues: {path name: issue number}."""
    prev = {e["name"]: e for e in previous_result["paths"]} if previous_result else {}
    actions = []
    for entry in result["paths"]:
        if entry.get("targetSeconds") is None:
            continue
        issue = open_issues.get(entry["name"])
        action, reason = decide(entry, prev.get(entry["name"]), issue)
        actions.append({"name": entry["name"], "action": action, "reason": reason, "issue": issue})
    return actions


# ---------------------------------------------------------------------------
# GitHub side effects (gh CLI; GH_TOKEN from the workflow)


def marker(name):
    return "%s%s -->" % (MARKER_PREFIX, name)


def find_open_issues(repo):
    out = gh(["issue", "list", "--repo", repo, "--state", "open", "--limit", "500",
              "--json", "number,body"])
    found = {}
    for issue in json.loads(out):
        body = issue.get("body") or ""
        start = body.find(MARKER_PREFIX)
        if start < 0:
            continue
        end = body.find(" -->", start)
        name = body[start + len(MARKER_PREFIX):end]
        # Exactly one per path: the oldest wins if a race ever made two.
        if name not in found or issue["number"] < found[name]:
            found[name] = issue["number"]
    return found


def measurement_table(entry, result, previous):
    prev_line = "unknown (no previous run)"
    if previous is not None:
        prev_line = "p95 %.3fs, %s (%s)" % (
            previous["p95Seconds"], "violated" if previous["violated"] else "held", previous.get("_measuredAt", ""))
    server = result.get("server", {})
    return "\n".join([
        "| field | value |",
        "| --- | --- |",
        "| path | `%s` |" % entry["path"],
        "| measured at | %s |" % result["measuredAt"],
        "| server | %s / %s |" % (server.get("version"), server.get("revision")),
        "| vantage | %s |" % result.get("vantage"),
        "| run | %s |" % run_link(result),
        "| server time median / p95 | %.3fs / %.3fs |" % (entry["medianSeconds"], entry["p95Seconds"]),
        "| target (server-time p95) | %.3fs |" % entry["targetSeconds"],
        "| raw TTFB p95 / round trip | %.3fs / %.3fs |" % (
            entry.get("ttfbP95Seconds") or 0, entry.get("rttMedianSeconds") or 0),
        "| failed requests | %d of %d (statuses %s) |" % (entry["errors"], entry["count"], ", ".join(entry["statuses"])),
        "| previous run | %s |" % prev_line,
        "",
        "Samples (server seconds, failures = null): `%s`" % json.dumps(entry["samples"]),
    ])


def run_link(result):
    run = result.get("run")
    server_url = os.environ.get("GITHUB_SERVER_URL", "https://github.com")
    repo = os.environ.get("GITHUB_REPOSITORY")
    if run and repo:
        return "%s/%s/actions/runs/%s" % (server_url, repo, run)
    return run or "local"


def cmd_reconcile(args):
    result = load_json(args.result)
    previous = None
    if args.previous and os.path.exists(args.previous):
        previous = load_json(args.previous)
        if previous.get("metric") != result.get("metric"):
            # A result on another scale cannot count toward a streak.
            print("previous result measured %s, not %s; ignoring it" % (previous.get("metric"), result.get("metric")))
            previous = None
        else:
            for e in previous["paths"]:
                e["_measuredAt"] = previous["measuredAt"]
    open_issues = find_open_issues(args.repo) if not args.dry_run else {}
    actions = plan(result, previous, open_issues)
    by_name = {e["name"]: e for e in result["paths"]}
    prev_by = {e["name"]: e for e in previous["paths"]} if previous else {}
    for a in actions:
        entry = by_name[a["name"]]
        table = measurement_table(entry, result, prev_by.get(a["name"]))
        print("%s: %s (%s)" % (a["name"], a["action"], a["reason"]))
        if args.dry_run or a["action"] == "none":
            continue
        if a["action"] == "open":
            body = "\n\n".join([
                marker(a["name"]),
                "The p95 server time of `%s` was above its SLO target on two consecutive "
                "runs of the performance SLO probe (#511)." % entry["path"],
                table,
                "This Issue stays open until the SLO holds on two consecutive runs; the probe "
                "closes it then. Baseline and targets: `scripts/perf-slo-baseline.json`; "
                "method: `docs/performance-slo.md`.",
            ])
            gh(["issue", "create", "--repo", args.repo,
                "--title", "[SLO] p95 violation: %s" % entry["path"], "--body", body])
        elif a["action"] == "update":
            gh(["issue", "comment", str(a["issue"]), "--repo", args.repo,
                "--body", "Performance SLO probe: %s.\n\n%s" % (a["reason"], table)])
        elif a["action"] == "close":
            gh(["issue", "close", str(a["issue"]), "--repo", args.repo,
                "--comment", "Performance SLO probe: %s; closing.\n\n%s" % (a["reason"], table)])
    if args.actions_out:
        write_json(args.actions_out, actions)
    return 0


def gh(argv):
    return subprocess.run(["gh"] + argv, check=True, capture_output=True, text=True).stdout


# ---------------------------------------------------------------------------


def utc_now():
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def load_json(path):
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def write_json(path, value):
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(value, f, indent=2)
        f.write("\n")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = parser.add_subparsers(dest="cmd", required=True)

    m = sub.add_parser("measure")
    m.add_argument("--config", default="scripts/perf-slo-baseline.json")
    m.add_argument("--base-url")
    m.add_argument("--rounds", type=int)
    m.add_argument("--vantage", default="unspecified")
    m.add_argument("--out", required=True)
    m.set_defaults(func=cmd_measure)

    b = sub.add_parser("baseline")
    b.add_argument("--config", default="scripts/perf-slo-baseline.json")
    b.add_argument("--result", required=True)
    b.add_argument("--same-vantage-result")
    b.set_defaults(func=cmd_baseline)

    r = sub.add_parser("reconcile")
    r.add_argument("--result", required=True)
    r.add_argument("--previous")
    r.add_argument("--repo", required=True)
    r.add_argument("--dry-run", action="store_true")
    r.add_argument("--actions-out")
    r.set_defaults(func=cmd_reconcile)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
