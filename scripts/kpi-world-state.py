#!/usr/bin/env python3
"""GOAP world state for the CodeSampleX pilot KPI (#800).

    python3 scripts/kpi-world-state.py --base-url https://codesamplex.dev
    python3 scripts/kpi-world-state.py --stats-file stats.json --now 2026-11-24T00:00:00Z
    python3 scripts/kpi-world-state.py --base-url https://codesamplex.dev --expected 5

Prints one JSON object. Exits 0 when the measurement is fresh, 1 when the
stats clock is older than --limit-hours, 2 when no measurement could be read.
"""

import argparse
import datetime as dt
import json
import sys
import urllib.request

METRIC = "activeInstallations7d"
BASELINE = 3
TARGET = 10
JUDGMENT_AT = "2026-11-24T09:00:00+09:00"
DEFAULT_LIMIT_HOURS = 24
TIMEOUT_SECONDS = 20


def parse_stamp(value, name):
    if not isinstance(value, str) or not value.strip():
        raise ValueError("missing %s" % name)
    text = value.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    stamp = dt.datetime.fromisoformat(text)
    if stamp.tzinfo is None:
        raise ValueError("%s carries no timezone: %r" % (name, value))
    return stamp.astimezone(dt.timezone.utc)


def utc(stamp):
    return stamp.strftime("%Y-%m-%dT%H:%M:%SZ")


def count(stats, key):
    value = stats.get(key)
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        return None
    return value


def verdict(current, target, now, judgment):
    if current is None:
        return "unknown"
    if current >= target:
        return "achieved"
    return "missed" if now >= judgment else "pending"


def evaluate(stats, now, target=TARGET, baseline=BASELINE, judgment_at=JUDGMENT_AT,
             limit_hours=DEFAULT_LIMIT_HOURS, expected=None):
    if not isinstance(stats, dict):
        raise ValueError("stats document is not an object")
    current = count(stats, METRIC)
    if current is None:
        raise ValueError("missing or invalid %s" % METRIC)
    generated = parse_stamp(stats.get("generatedAt"), "generatedAt")
    judgment = parse_stamp(judgment_at, "judgmentAt")
    age = int((now - generated).total_seconds())
    fresh = 0 <= age <= int(limit_hours * 3600)
    seconds_left = int((judgment - now).total_seconds())
    result = {
        "check": "goap-kpi-world-state",
        "observedAt": utc(now),
        "fresh": fresh,
        "statsAgeSeconds": age,
        "pulse": {
            "activeInstalls7d": current,
            "activeInstalls1d": count(stats, "activeInstallations1d"),
            "activeInstalls30d": count(stats, "activeInstallations30d"),
            "measuredAt": utc(generated),
            "source": "measured",
        },
        "kpi": {
            "metric": METRIC,
            "baseline": baseline,
            "current": current,
            "target": target,
            "gap": max(0, target - current),
            "judgmentAt": judgment_at,
            "daysLeft": max(0, -(-seconds_left // 86400)),
            "verdict": verdict(current, target, now, judgment),
        },
    }
    if expected is not None:
        result["replan"] = {"expected": expected, "actual": current, "required": current < expected}
    return result


def fetch_json(url):
    request = urllib.request.Request(url, headers={"User-Agent": "csx-kpi-world-state/1"})
    with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
        return json.load(response)


def unknown(now, error, judgment_at):
    return {
        "check": "goap-kpi-world-state",
        "observedAt": utc(now),
        "fresh": False,
        "error": error,
        "pulse": {
            "activeInstalls7d": None,
            "activeInstalls1d": None,
            "activeInstalls30d": None,
            "measuredAt": None,
            "source": "unknown",
        },
        "kpi": {"metric": METRIC, "current": None, "judgmentAt": judgment_at, "verdict": "unknown"},
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description="GOAP world state for the CodeSampleX pilot KPI")
    parser.add_argument("--base-url", help="server to read GET /v1/stats from")
    parser.add_argument("--stats-file", help="read a saved /v1/stats document instead of fetching one")
    parser.add_argument("--now", help="evaluation time (RFC 3339); default is the current time")
    parser.add_argument("--target", type=int, default=TARGET)
    parser.add_argument("--baseline", type=int, default=BASELINE)
    parser.add_argument("--judgment-at", default=JUDGMENT_AT)
    parser.add_argument("--limit-hours", type=float, default=DEFAULT_LIMIT_HOURS)
    parser.add_argument("--expected", type=int, help="KPI value the finished node expected; sets replan.required")
    args = parser.parse_args(argv)
    if bool(args.base_url) == bool(args.stats_file):
        parser.error("give exactly one of --base-url or --stats-file")

    now = parse_stamp(args.now, "--now") if args.now else dt.datetime.now(dt.timezone.utc)
    try:
        if args.base_url:
            stats = fetch_json(args.base_url.rstrip("/") + "/v1/stats")
        else:
            with open(args.stats_file, encoding="utf-8") as handle:
                stats = json.load(handle)
        result = evaluate(stats, now, args.target, args.baseline, args.judgment_at,
                          args.limit_hours, args.expected)
    except Exception as exc:  # noqa: BLE001 - reported, not raised
        print(json.dumps(unknown(now, str(exc), args.judgment_at), sort_keys=True))
        return 2
    print(json.dumps(result, sort_keys=True))
    return 0 if result["fresh"] else 1


if __name__ == "__main__":
    sys.exit(main())
