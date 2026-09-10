#!/usr/bin/env python3
"""Bounded PostgreSQL metrics with static, heuristic source attribution.

SQL text remains inside PostgreSQL: only numeric metrics and catalog labels
are returned. pg_stat_statements normalization is NOT a secrets boundary.
"""
import argparse
import json
import math
from pathlib import Path
import subprocess
import sys

SORT_KEYS = ("total_exec_time", "mean_exec_time", "max_exec_time", "calls")
METRICS = ("queryid", "calls", "total_ms", "mean_ms", "max_ms", "io_ms",
           "shared_blks_hit", "shared_blks_read", "hit_pct", "temp_blks", "rows")
# Patterns are PostgreSQL regular expressions, not interpolated user input.
# More specific CTEs precede broader families. Attribution is only a hint.
CATALOG = (
    (r"WITH verified_samples AS MATERIALIZED.*verified_symbols",
     "AuthoringExpansionCandidates", "internal/serverstore/authoring_pg.go"),
    (r"WITH verified_samples AS MATERIALIZED",
     "Authoring coverage/backlog family", "internal/serverstore/dependencyclosure_pg.go"),
    (r"failure_clusters.*WHERE.*package_name[[:space:]]*=",
     "ListFailureClusters", "internal/serverstore/pg.go"),
    (r"WITH wanted_key AS MATERIALIZED", "ListWanted", "internal/serverstore/pg.go"),
    (r"compatibility_snapshots.*WHERE.*purl[[:space:]]*=",
     "Snapshot lookup family", "internal/serverstore/pg.go"),
    (r"SELECT sample_id.*FROM samples.*WHERE NOT quarantined",
     "ListSamples / ListSamplesPage", "internal/serverstore/pg.go"),
)
DEFAULT_COMPOSE = str(Path(__file__).resolve().parents[1] / "deploy" / "docker-compose.yml")


def bounded_limit(value):
    try:
        number = int(value)
    except ValueError:
        raise argparse.ArgumentTypeError("limit must be an integer") from None
    if not 1 <= number <= 100:
        raise argparse.ArgumentTypeError("limit must be between 1 and 100")
    return number


class SafeParser(argparse.ArgumentParser):
    def error(self, message):
        # argparse normally echoes rejected values, which may contain secrets.
        self.print_usage(sys.stderr)
        self.exit(2, "Invalid arguments; use --help for supported options.\n")


def query_sql(sort, limit):
    if sort not in SORT_KEYS or not isinstance(limit, int) or not 1 <= limit <= 100:
        raise ValueError("invalid metrics bounds")
    cases = " ".join(
        "WHEN query ~* '" + pattern.replace("'", "''") + "' THEN " + str(index)
        for index, (pattern, _, _) in enumerate(CATALOG)
    )
    return f"""
SELECT coalesce(json_agg(t), '[]'::json) FROM (
  SELECT queryid, calls,
    round(total_exec_time::numeric, 2) AS total_ms,
    round(mean_exec_time::numeric, 2) AS mean_ms,
    round(max_exec_time::numeric, 2) AS max_ms,
    round((shared_blk_read_time + shared_blk_write_time)::numeric, 2) AS io_ms,
    shared_blks_hit, shared_blks_read,
    round(shared_blks_hit::numeric /
      nullif(shared_blks_hit + shared_blks_read, 0) * 100, 1) AS hit_pct,
    temp_blks_read + temp_blks_written AS temp_blks, rows,
    CASE {cases} ELSE -1 END AS attribution
  FROM public.pg_stat_statements
  WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  ORDER BY {sort} DESC, queryid
  LIMIT {limit}
) t;
"""


def run_sql(compose, sql):
    cmd = ["docker", "compose", "-f", compose, "exec", "-T", "db", "psql",
           "-X", "-v", "ON_ERROR_STOP=1", "-U", "csx", "-d", "csx", "-Atqc",
           "SET statement_timeout='5s'; SET lock_timeout='1s'; " + sql]
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=15)
    except (OSError, subprocess.TimeoutExpired):
        raise RuntimeError("Database command unavailable or timed out.") from None
    if proc.returncode:
        # stderr can contain SQL, connection strings, or literal values.
        raise RuntimeError("Database command failed; verify Compose access, preload, and extension setup.") from None
    return proc.stdout.strip()


def check_extension(compose):
    # Function call also verifies the module was preloaded at server startup.
    raw = run_sql(compose, """
SELECT json_build_object('installed',
  EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements'),
  'entries', (SELECT count(*) FROM public.pg_stat_statements(false)));
""")
    result = json.loads(raw)
    if not isinstance(result, dict) or result.get("installed") is not True:
        raise ValueError("extension not ready")
    entries = result.get("entries")
    if type(entries) is not int or entries < 0:
        raise ValueError("invalid extension response")
    return {"installed": True, "entries": entries}


def fetch_metrics(compose, sort, limit):
    data = json.loads(run_sql(compose, query_sql(sort, limit)))
    if not isinstance(data, list) or len(data) > limit:
        raise ValueError("invalid metrics response")
    output = []
    for row in data:
        if not isinstance(row, dict):
            raise ValueError("invalid metrics row")
        item = {}
        for key in METRICS:
            value = row.get(key)
            if value is not None and (type(value) not in (int, float) or not math.isfinite(value)):
                raise ValueError("invalid metric")
            item[key] = value
        index = row.get("attribution")
        if type(index) is int and 0 <= index < len(CATALOG):
            _, method, source = CATALOG[index]
        else:
            method, source = "Unknown / uncataloged", ""
        item.update(attributed_method=method, source_file=source)
        output.append(item)
    return output


def main(argv=None):
    parser = SafeParser(description=__doc__)
    parser.add_argument("sort", nargs="?", choices=SORT_KEYS, default=SORT_KEYS[0])
    parser.add_argument("limit", nargs="?", type=bounded_limit, default=15)
    parser.add_argument("--json", action="store_true", help="emit metrics and static labels as JSON")
    parser.add_argument("--compose-file", default=DEFAULT_COMPOSE)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="verify extension and preload without mutations")
    mode.add_argument("--init", action="store_true", help="explicitly create the extension, then verify it")
    args = parser.parse_args(argv)
    try:
        if args.init:
            run_sql(args.compose_file,
                    "CREATE EXTENSION IF NOT EXISTS pg_stat_statements WITH SCHEMA public;")
        if args.init or args.check:
            result = check_extension(args.compose_file)
        else:
            result = fetch_metrics(args.compose_file, args.sort, args.limit)
    except (RuntimeError, ValueError, TypeError, OverflowError):
        print("Diagnostics failed. Verify database access and pg_stat_statements setup with --check; see docs/operations.md.", file=sys.stderr)
        return 1
    if args.json or args.init or args.check:
        print(json.dumps(result, indent=2, allow_nan=False))
    else:
        print("QueryID | Calls | Total ms | Mean ms | Max ms | IO ms | Hit % | Method (heuristic)")
        for item in result:
            print(" | ".join(str(item[key]) for key in
                            ("queryid", "calls", "total_ms", "mean_ms", "max_ms",
                             "io_ms", "hit_pct", "attributed_method")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
