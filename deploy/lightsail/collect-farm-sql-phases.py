#!/usr/bin/env python3
"""Fixed read-only SQL-family sampler; loaded with reviewed transport in memory."""
import json
import re
import sys
import time

import _farm_bounded_transport as transport

CATALOG_REVISION = "2b859134e5a9708a76be6e148af2034d9a57cc5e"
SAMPLE_SECONDS, INTERVAL_SECONDS, MAX_SAMPLES = 90, 2, 45
TOTAL_SECONDS, COMMAND_SECONDS = 105, 3
MAX_COMMAND_BYTES, MAX_SUMMARY_BYTES = 16384, 768 * 1024
MAX_ACTIVE, MAX_AGE_MS = 128, 86400000
PHASES = ("workers", "health", "backlog_stocks", "matrix", "claims", "first_pass", "completeness", "coverage")
WAITS = ("running", "Activity", "BufferPin", "Client", "Extension", "IO", "IPC", "Lock", "LWLock", "Timeout", "other")
UNKNOWN = ("unclassified", "truncated", "ambiguous")
FAILURES = ("none", "not_collected", "invalid_expected_revision", "unsupported_catalog", "revision_mismatch",
            "identity_changed", "invalid_identity", "invalid_capabilities", "invalid_sample", "invalid_summary",
            "command_failed", "command_timeout", "byte_limit", "window_timeout", "clock_changed",
            "transport_failed", "collector_failed")
# SHA-256 of complete normalized statements at CATALOG_REVISION. This is a
# statement-family fingerprint, not a query ID or proof of an HTTP caller.
FINGERPRINTS = (
    ("555adad00946127a0c814945433de1605431638c0afdd6f786954f55d5db4528", "workers"),
    ("51e46e271afecca568d8895abeb5dc75ff8bc79d918f972057cf7b5bdb5384d0", "health"),
    ("ae9d6e421bb72ffcbdc78969eaa5be4adc2edeec4c7291b61142b2103c09a2a9", "health"),
    ("a6e4176c5b4f1961db1fcd1b4b8593a757968bbe4f0518e288c6601e656da021", "health"),
    ("61bad6c1fac4e64e9307b06f1f66ce07c16377b26d4415790b5974f7df13c9c8", "health"),
    ("95ea1c905855caa92ca41cc68133a247fd8b7d14cc1dc128ad60bab4eb066b9e", "health"),
    ("883ceebb5abc22ff15662ffbcc65db2bb256dabb2ec5ce4997d8218a7fda892d", "health"),
    ("0f2b35d0c2121e72928baeff25c7b749221bd8f894cc4f51d440bd63ffa45822", "health"),
    ("cf08c0f3795477660adcf547ccd0368bf5f34f34c59c8d9a5fd3f1d2e6487de1", "coverage"),
    ("155bb6bb8b5564373579c75616c478689349c672dd326e6a1d756f5df82cee77", "backlog_stocks"),
    ("e9442adf8915bd8e41ad8675f864f4164256d51324a9cbac0cd301ab24709a4a", "claims"),
    ("5e745675bc4ad8f9294eea8de7d3b8385192318214884e3c93dbb7f3f1d14123", "first_pass"),
    ("5eed0862ec22da5e74c74a15fda23c622dc21e87d5a22ff5b95274e056bfd0af", "completeness"),
    ("2d10ce078ce1a92e5295ba18f8a884ba3041d3dc91227dc61ee3467eca4c1b30", "matrix"),
)
# One lexical pass protects quotes/comments and identifier digits. Unsupported
# characters fail reconstruction instead of being silently removed. Dollar-
# quoted strings, escaped strings and quoted identifiers are outside this fixed
# source catalog; they cannot acquire a known fingerprint by normalization.
TOKEN_PATTERN = r"((?:--[^\r\n]*)|(?:'(?:[^']|'')*')|(?:\$[0-9]+)|(?:[A-Za-z_][A-Za-z_0-9]*)|(?:[0-9]+)|(?:[[:space:]]+)|(?:::|->>|->|<>|>=|<=|!=|\|\||[(),.*=+/?<>-]))"


class Unavailable(Exception):
    def __init__(self, reason):
        self.reason = reason if reason in FAILURES else "collector_failed"


def require(ok, reason="invalid_summary"):
    if not ok:
        raise Unavailable(reason)


def keys(value, expected):
    require(type(value) is dict and set(value) == set(expected))


def integer(value, low, high):
    require(type(value) is int and low <= value <= high)


def unique(pairs):
    out = {}
    for key, value in pairs:
        require(key not in out)
        out[key] = value
    return out


def decode(raw, limit=MAX_COMMAND_BYTES):
    require(type(raw) is bytes and len(raw) <= limit and raw.endswith(b"\n"))
    return json.loads(raw.decode("utf-8", "strict"), object_pairs_hook=unique,
                      parse_constant=lambda _: require(False))


def empty(reason="not_collected", expected=None):
    return {"schemaVersion": 1, "availability": "unavailable", "failureClass": reason,
            "expectedRevision": expected if transport.valid_revision(expected) else None,
            "catalogRevision": CATALOG_REVISION, "scope": "active_sql_families_not_http_request_attribution",
            "sampleWindowSeconds": SAMPLE_SECONDS, "intervalSeconds": INTERVAL_SECONDS,
            "identity": None, "capabilities": None, "window": None, "samples": None}


def validate_capabilities(value):
    keys(value, ("trackActivityQuerySize", "computeQueryId", "pgssPublic", "pgssPreloaded"))
    integer(value["trackActivityQuerySize"], 100, 1048576)
    require(value["computeQueryId"] in ("auto", "on", "off", "unknown"))
    require(type(value["pgssPublic"]) is bool and type(value["pgssPreloaded"]) is bool)
    return value


def validate_sample(value):
    keys(value, ("active", "unknown", "phases"))
    integer(value["active"], 0, MAX_ACTIVE)
    keys(value["unknown"], UNKNOWN)
    for n in value["unknown"].values():
        integer(n, 0, value["active"])
    require(type(value["phases"]) is list and len(value["phases"]) <= len(PHASES) * len(WAITS))
    seen, total = set(), sum(value["unknown"].values())
    for row in value["phases"]:
        keys(row, ("phase", "waitClass", "count", "minAgeMs", "maxAgeMs", "ageCapped"))
        require(row["phase"] in PHASES and row["waitClass"] in WAITS)
        pair = (row["phase"], row["waitClass"])
        require(pair not in seen)
        seen.add(pair)
        integer(row["count"], 1, value["active"])
        integer(row["minAgeMs"], 0, MAX_AGE_MS)
        integer(row["maxAgeMs"], row["minAgeMs"], MAX_AGE_MS)
        require(type(row["ageCapped"]) is bool)
        require(not row["ageCapped"] or row["maxAgeMs"] == MAX_AGE_MS)
        total += row["count"]
    require(total == value["active"])
    return value


def validate_identity(value, private=False):
    keys(value, ("id", "imageDigest", "revision", "startedAt") if private else ("imageDigest", "revision", "startedAt"))
    if private:
        require(type(value["id"]) is str and re.fullmatch(r"[0-9a-f]{64}", value["id"]) is not None)
    require(type(value["imageDigest"]) is str and re.fullmatch(r"sha256:[0-9a-f]{64}", value["imageDigest"]) is not None)
    require(value["revision"] == CATALOG_REVISION)
    transport.timestamp_ns(value["startedAt"])
    return value


def validate(raw, expected):
    value = decode(raw, MAX_SUMMARY_BYTES)
    template = empty(expected=expected)
    keys(value, template)
    for key in ("schemaVersion", "expectedRevision", "catalogRevision", "scope", "sampleWindowSeconds", "intervalSeconds"):
        require(type(value[key]) is type(template[key]) and value[key] == template[key])
    require(value["availability"] in ("available", "unavailable") and value["failureClass"] in FAILURES)
    if value["availability"] == "unavailable":
        require(value["failureClass"] != "none")
        require(all(value[k] is None for k in ("identity", "capabilities", "window", "samples")))
        return value
    require(expected == CATALOG_REVISION and value["failureClass"] == "none")
    validate_identity(value["identity"])
    validate_capabilities(value["capabilities"])
    window = value["window"]
    keys(window, ("startedAt", "finishedAt", "elapsedMs", "variant"))
    start, finish = (transport.timestamp_ns(window[k]) for k in ("startedAt", "finishedAt"))
    integer(window["elapsedMs"], SAMPLE_SECONDS * 1000, SAMPLE_SECONDS * 1000 + 3000)
    require(0 <= finish - start <= (SAMPLE_SECONDS + 4) * 1000000000)
    require(window["variant"] in ("activity_only", "activity_with_pgss"))
    require(window["variant"] == variant(value["capabilities"]))
    require(type(value["samples"]) is list and 1 <= len(value["samples"]) <= MAX_SAMPLES)
    previous = -INTERVAL_SECONDS * 1000
    for sample in value["samples"]:
        keys(sample, ("startedAt", "finishedAt", "offsetMs", "durationMs", "counts"))
        integer(sample["offsetMs"], 0, SAMPLE_SECONDS * 1000 - 1)
        integer(sample["durationMs"], 0, COMMAND_SECONDS * 1000 + 250)
        require(sample["offsetMs"] >= previous + INTERVAL_SECONDS * 1000 - 1)
        previous = sample["offsetMs"]
        a, b = (transport.timestamp_ns(sample[k]) for k in ("startedAt", "finishedAt"))
        require(start <= a <= b <= finish)
        validate_sample(sample["counts"])
    return value


CAPABILITY_SQL = """SELECT json_build_object(
 'trackActivityQuerySize', (SELECT setting::int FROM pg_settings WHERE name='track_activity_query_size'),
 'computeQueryId', CASE WHEN current_setting('compute_query_id') IN ('auto','on','off')
                       THEN current_setting('compute_query_id') ELSE 'unknown' END,
 'pgssPublic', EXISTS (SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
                      WHERE e.extname='pg_stat_statements' AND n.nspname='public')
               AND to_regclass('public.pg_stat_statements') IS NOT NULL,
 'pgssPreloaded', 'pg_stat_statements'=ANY(string_to_array(replace(current_setting('shared_preload_libraries'),' ',''),',')))"""


def variant(capabilities):
    return "activity_with_pgss" if (capabilities["pgssPublic"] and capabilities["pgssPreloaded"]
                                   and capabilities["computeQueryId"] in ("auto", "on")) else "activity_only"


def fingerprint_sql(expression):
    return """(SELECT CASE WHEN string_agg(m[1],'' ORDER BY n)=""" + expression + """ THEN
      encode(sha256(convert_to(string_agg(CASE
        WHEN m[1] LIKE '--%' OR m[1] ~ '^[[:space:]]+$' THEN NULL
        WHEN left(m[1],1) IN ('''','$') OR m[1] ~ '^[0-9]+$' THEN '?'
        ELSE lower(m[1]) END,' ' ORDER BY n),'UTF8')),'hex') END
      FROM regexp_matches(""" + expression + ", $farm_lex$" + TOKEN_PATTERN + "$farm_lex$, 'g') WITH ORDINALITY AS lex(m,n))"


def sampling_sql(with_pgss):
    require(type(with_pgss) is bool)
    catalog = ",".join("('%s','%s')" % item for item in FINGERPRINTS)
    # Function text retrieval is behind a CASE: no pgss text scan when no
    # truncated active statement needs it. Its read can still touch the whole
    # pgss text file; the 1s statement deadline applies and any failure stops.
    extra = ""
    representatives = """SELECT a.pid, NULL::text AS family, false AS ambiguous FROM activity a"""
    if with_pgss:
        extra = """, needed AS MATERIALIZED (
          SELECT DISTINCT datid,usesysid,query_id FROM activity WHERE truncated AND query_id IS NOT NULL AND query_id<>0
        ), statement_text AS MATERIALIZED (
          SELECT x.* FROM jsonb_to_recordset(CASE WHEN EXISTS (SELECT 1 FROM needed) THEN
            (SELECT COALESCE(jsonb_agg(jsonb_build_object('dbid',s.dbid,'userid',s.userid,'queryid',s.queryid,'fingerprint',""" + fingerprint_sql("s.query") + """)), '[]'::jsonb)
             FROM public.pg_stat_statements(true) s
             WHERE EXISTS (SELECT 1 FROM needed n WHERE s.dbid=n.datid AND s.userid=n.usesysid AND s.queryid=n.query_id))
            ELSE '[]'::jsonb END) AS x(dbid oid, userid oid, queryid bigint, fingerprint text)
        )"""
        representatives = """SELECT a.pid,
          CASE WHEN count(p.queryid)>0 AND bool_and(c.phase IS NOT NULL)
                     AND count(DISTINCT c.phase)=1 THEN min(c.phase) END AS family,
          count(p.queryid)>0 AND (NOT bool_and(c.phase IS NOT NULL) OR count(DISTINCT c.phase)<>1) AS ambiguous
        FROM activity a LEFT JOIN statement_text p ON a.truncated AND p.dbid=a.datid AND p.userid=a.usesysid AND p.queryid=a.query_id
        LEFT JOIN catalog c ON c.fingerprint=p.fingerprint GROUP BY a.pid"""
    return """WITH catalog(fingerprint,phase) AS (VALUES """ + catalog + """),
      activity AS MATERIALIZED (
        SELECT pid, datid, usesysid, query_id, query,
          -- UTF-8 clipping may stop up to three bytes before the byte limit.
          -- Treat the whole boundary band as potentially truncated.
          octet_length(query)>=(SELECT setting::int FROM pg_settings WHERE name='track_activity_query_size')-4 AS truncated,
          CASE WHEN wait_event_type IS NULL THEN 'running'
               WHEN wait_event_type IN ('Activity','BufferPin','Client','Extension','IO','IPC','Lock','LWLock','Timeout')
               THEN wait_event_type ELSE 'other' END AS wait_class,
          GREATEST(0, floor(extract(epoch FROM (statement_timestamp()-query_start))*1000)) AS age_ms
        FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND pid<>pg_backend_pid()
      )""" + extra + """, representatives AS (""" + representatives + """), classified AS (
        SELECT a.pid, a.wait_class, a.age_ms,
          CASE WHEN NOT a.truncated THEN c.phase ELSE p.family END AS phase,
          CASE WHEN p.ambiguous THEN 'ambiguous' WHEN a.truncated THEN 'truncated' ELSE 'unclassified' END AS unknown
        FROM activity a LEFT JOIN catalog c ON NOT a.truncated AND c.fingerprint=""" + fingerprint_sql("a.query") + """
        JOIN representatives p ON p.pid=a.pid
      ), grouped AS (
        SELECT phase, wait_class, count(*) AS n, LEAST(min(age_ms),86400000)::bigint AS low,
          LEAST(max(age_ms),86400000)::bigint AS high, bool_or(age_ms>86400000) AS capped
        FROM classified WHERE phase IS NOT NULL GROUP BY phase,wait_class
      ) SELECT json_build_object('active',(SELECT count(*) FROM activity),
        'unknown',json_build_object(
          'unclassified',(SELECT count(*) FROM classified WHERE phase IS NULL AND unknown='unclassified'),
          'truncated',(SELECT count(*) FROM classified WHERE phase IS NULL AND unknown='truncated'),
          'ambiguous',(SELECT count(*) FROM classified WHERE phase IS NULL AND unknown='ambiguous')),
        'phases',COALESCE((SELECT json_agg(json_build_object('phase',phase,'waitClass',wait_class,
          'count',n,'minAgeMs',low,'maxAgeMs',high,'ageCapped',capped) ORDER BY phase,wait_class) FROM grouped),'[]'::json))"""


def sql_command(sql):
    # Existing canonical compose/db/role route; no credential read or export.
    return ("docker", "compose", "-f", "/opt/codesamplex/deploy/docker-compose.yml", "exec", "-T",
            "-e", "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=1000 -c lock_timeout=500 -c jit=off -c standard_conforming_strings=on",
            "-e", "PGCONNECT_TIMEOUT=2", "db", "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "csx", "-d", "csx", "-Atqc", sql)


def collect(expected, run=transport.bounded_command, monotonic=time.monotonic, wall=time.time_ns, sleep=time.sleep):
    result = empty(expected=expected)
    if not transport.valid_revision(expected):
        return empty("invalid_expected_revision")
    if expected != CATALOG_REVISION:
        return empty("unsupported_catalog", expected)
    total_deadline = monotonic() + TOTAL_SECONDS

    def command(argv):
        budget = min(COMMAND_SECONDS, total_deadline - monotonic())
        require(budget == COMMAND_SECONDS, "window_timeout")
        return run(argv, budget, MAX_COMMAND_BYTES)

    def identity():
        raw = command(transport.INSPECT)
        try:
            value = decode(raw)
            require(type(value) is dict, "invalid_identity")
            if value.get("revision") != expected:
                raise Unavailable("revision_mismatch")
            return validate_identity(value, private=True)
        except Unavailable as exc:
            if exc.reason == "revision_mismatch":
                raise
            raise Unavailable("invalid_identity") from None
        except Exception:
            raise Unavailable("invalid_identity") from None

    try:
        before = identity()
        raw = command(sql_command(CAPABILITY_SQL))
        try:
            capabilities = validate_capabilities(decode(raw))
        except Exception:
            raise Unavailable("invalid_capabilities") from None
        selected = variant(capabilities)
        sql = sampling_sql(selected == "activity_with_pgss")
        started, started_wall = monotonic(), wall()
        deadline, next_at, samples = started + SAMPLE_SECONDS, started, []
        while len(samples) < MAX_SAMPLES:
            remaining = next_at - monotonic()
            if remaining > 0:
                sleep(remaining)
            at = monotonic()
            if at >= deadline - COMMAND_SECONDS:
                break
            at_wall = wall()
            raw = command(sql_command(sql))
            finished, finished_wall = monotonic(), wall()
            require(finished_wall >= at_wall and abs((finished_wall-at_wall)/1e9-(finished-at)) <= 1, "clock_changed")
            try:
                counts = validate_sample(decode(raw))
            except Exception:
                raise Unavailable("invalid_sample") from None
            samples.append({"startedAt": transport.stamp(at_wall), "finishedAt": transport.stamp(finished_wall),
                            "offsetMs": int((at-started)*1000), "durationMs": int((finished-at)*1000), "counts": counts})
            # Never catch up in a burst after a slow command.
            next_at = max(at + INTERVAL_SECONDS, finished)
        remaining = deadline - monotonic()
        if remaining > 0:
            sleep(remaining)
        finished, finished_wall = monotonic(), wall()
        require(finished <= deadline + COMMAND_SECONDS, "window_timeout")
        require(finished_wall >= started_wall and abs((finished_wall-started_wall)/1e9-(finished-started)) <= 1, "clock_changed")
        after = identity()
        require(monotonic() <= total_deadline, "window_timeout")
        require(before == after, "identity_changed")
        result.update(availability="available", failureClass="none", identity={k: before[k] for k in ("imageDigest", "revision", "startedAt")},
                      capabilities=capabilities, window={"startedAt": transport.stamp(started_wall), "finishedAt": transport.stamp(finished_wall),
                      "elapsedMs": int((finished-started)*1000), "variant": selected}, samples=samples)
        return validate((json.dumps(result, separators=(",", ":"))+"\n").encode(), expected)
    except (Unavailable, transport.Unavailable) as exc:
        return empty(exc.reason if exc.reason in FAILURES else "collector_failed", expected)
    except Exception:
        return empty("collector_failed", expected)


if __name__ == "__main__":
    summary = collect(sys.argv[1] if len(sys.argv) == 2 else "")
    # A valid unavailable summary is still transported. The runner records it
    # and fails its gate; stderr and exception text are never published.
    print(json.dumps(summary, separators=(",", ":"), ensure_ascii=True))
