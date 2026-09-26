#!/usr/bin/env python3
"""Builder freshness check (#517): is the public stats clock under a day old?

GET /v1/stats carries generatedAt, the start of the last builder pass that
finished. From 2026-09-17T12:34:46Z production kept serving that one stamp for
more than eight days while every probe of the site's latency stayed green,
because no check looked at the clock itself. This one does: a generatedAt more
than 24 hours old (the builder's resume window) is a violation.

    python3 scripts/stats-freshness.py --base-url https://codesamplex.dev
    python3 scripts/stats-freshness.py --generated-at 2026-09-17T12:34:46Z \
        --now 2026-09-26T01:50:00Z

It prints one JSON object and exits 0 when fresh, 1 when stale, 2 when the
stamp could not be read at all. When the server has GET /v1/builder, the
builder's own last-failure reason is attached so the alert says why.
"""

import argparse
import datetime as dt
import json
import sys
import urllib.request

DEFAULT_LIMIT_HOURS = 24
TIMEOUT_SECONDS = 20


def parse_stamp(value):
    if not isinstance(value, str) or not value:
        raise ValueError("missing generatedAt")
    text = value.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    stamp = dt.datetime.fromisoformat(text)
    if stamp.tzinfo is None:
        raise ValueError("generatedAt carries no timezone: %r" % value)
    return stamp.astimezone(dt.timezone.utc)


def evaluate(generated_at, now, limit_hours=DEFAULT_LIMIT_HOURS):
    """Judge one stamp. A stamp from the future is not fresh either: it is a
    clock nobody can trust, and it would hide a stall indefinitely."""
    stamp = parse_stamp(generated_at)
    age = int((now - stamp).total_seconds())
    limit = int(limit_hours * 3600)
    return {
        "check": "stats-generatedAt-age",
        "generatedAt": stamp.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "observedAt": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "ageSeconds": age,
        "ageDays": round(age / 86400, 2),
        "limitSeconds": limit,
        "ok": 0 <= age <= limit,
    }


def fetch_json(url):
    request = urllib.request.Request(url, headers={"User-Agent": "csx-stats-freshness/1"})
    with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
        return json.load(response)


def builder_reason(base_url):
    """Best effort: an older server has no /v1/builder, and the verdict never
    depends on it."""
    try:
        doc = fetch_json(base_url.rstrip("/") + "/v1/builder")
    except Exception:  # noqa: BLE001 - diagnostic only
        return None
    keep = ("lastPassOutcome", "lastSuccessAt", "lastFailureAt", "lastFailureReason",
            "lastFailurePhase", "consecutiveFailures", "repair")
    return {k: doc[k] for k in keep if k in doc}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--base-url", help="server to read GET /v1/stats from")
    parser.add_argument("--generated-at", help="judge this stamp instead of fetching one")
    parser.add_argument("--now", help="evaluation time (RFC 3339); default is the current time")
    parser.add_argument("--limit-hours", type=float, default=DEFAULT_LIMIT_HOURS)
    args = parser.parse_args(argv)
    if bool(args.base_url) == bool(args.generated_at):
        parser.error("give exactly one of --base-url or --generated-at")

    now = parse_stamp(args.now) if args.now else dt.datetime.now(dt.timezone.utc)
    try:
        stamp = args.generated_at
        if args.base_url:
            stamp = fetch_json(args.base_url.rstrip("/") + "/v1/stats").get("generatedAt")
        result = evaluate(stamp, now, args.limit_hours)
    except Exception as exc:  # noqa: BLE001 - reported, not raised
        print(json.dumps({"check": "stats-generatedAt-age", "ok": False, "error": str(exc)}))
        return 2
    if args.base_url:
        builder = builder_reason(args.base_url)
        if builder is not None:
            result["builder"] = builder
    print(json.dumps(result, sort_keys=True))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    sys.exit(main())
