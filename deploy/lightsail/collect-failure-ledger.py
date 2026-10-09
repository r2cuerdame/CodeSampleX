#!/usr/bin/env python3
"""Exact failure-ledger coverage through small indexed pages, without DB writes.

One existing psql session holds a read-only repeatable-read snapshot. The
existing 20s SQL and 30s command budgets cover the entire walk, not each page.
No prefix can publish totals: exhausting the PK cursor and a successful COMMIT
are both required. Missing, timed-out or oversized evidence stays unmeasured.
"""
import json
import os
import selectors
import subprocess
import sys
import time

PAGE_ROWS = 2000
SQL_SECONDS = 20
COMMAND_SECONDS = 30
JSON_BYTES = 4096
SOURCE_ROWS = 10000
DETAIL_ROWS = 250000

PAGE_SQL = """
WITH cluster_rows AS MATERIALIZED (
  SELECT id, observation_count, evidence_quality, error_fp,
    evidence_breakdown IS NULL OR (pg_column_size(evidence_breakdown) <= 4096
      AND pg_column_compression(evidence_breakdown) IS NULL) AS within_json_budget,
    CASE WHEN pg_column_size(evidence_breakdown) <= 4096
           AND pg_column_compression(evidence_breakdown) IS NULL THEN evidence_breakdown END AS evidence_breakdown,
    __MODERN_EXPRESSION__ AS modern
  FROM failure_clusters __KEYSET__
  ORDER BY id LIMIT __PAGE_ROWS__
), current_clusters AS MATERIALIZED (
  SELECT fc.* FROM cluster_rows fc
  WHERE within_json_budget
    AND (COALESCE(fc.evidence_quality,'legacy-evidence-incomplete') NOT IN ('missing','legacy-evidence-incomplete')
         OR COALESCE(fc.error_fp,'') = '')
), cluster_totals AS MATERIALIZED (
  SELECT count(*) AS current_rows,
    COALESCE(SUM(fc.observation_count),0) AS observations,
    count(*) FILTER (WHERE fc.observation_count IS NULL OR fc.observation_count <= 0
      OR CASE WHEN jsonb_typeof(fc.evidence_breakdown) IS DISTINCT FROM 'object' THEN true
              ELSE fc.evidence_breakdown - ARRAY['complete','partial','missing','legacy-evidence-incomplete'] <> '{}'::jsonb END
      OR breakdown.invalid_value
      OR fc.observation_count::numeric <> breakdown.total) AS unbalanced
  FROM current_clusters fc
  CROSS JOIN LATERAL (
    SELECT COALESCE(SUM(CASE WHEN jsonb_typeof(item.value) = 'number'
                            THEN (item.value::text)::numeric ELSE 0 END),0) AS total,
      COALESCE(bool_or(item.value IS NOT NULL AND
        CASE WHEN jsonb_typeof(item.value) = 'number'
             THEN (item.value::text)::numeric < 0 ELSE true END),false) AS invalid_value
    FROM (VALUES (fc.evidence_breakdown->'complete'), (fc.evidence_breakdown->'partial'),
                 (fc.evidence_breakdown->'missing'), (fc.evidence_breakdown->'legacy-evidence-incomplete')) item(value)
  ) breakdown
)
SELECT jsonb_build_object(
 'rows', (SELECT count(*) FROM cluster_rows),
 'cursor', (SELECT max(id)::text FROM cluster_rows),
 'withinJsonBudget', (SELECT COALESCE(bool_and(within_json_budget),true) FROM cluster_rows),
 'currentRows', ct.current_rows, 'observations', ct.observations,
 'unbalanced', ct.unbalanced,
 'modern', (SELECT count(*) FROM cluster_rows WHERE modern))
FROM cluster_totals ct
"""

SOURCE_SQL = """
WITH source_rows AS MATERIALIZED (
 SELECT result, observation_count FROM evidence_agg LIMIT 10001
)
SELECT jsonb_build_object('rows',count(*),
 'fail',COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL'),0))
FROM source_rows
"""

DETAIL_SQL = """
WITH source_rows AS MATERIALIZED (
 SELECT result, observation_count, purl, symbol, evidence_quality
 FROM evidence_agg LIMIT 250001
), sample_rows AS MATERIALIZED (
 SELECT status FROM samples LIMIT 250001
), scope AS MATERIALIZED (
 SELECT (SELECT count(*) FROM source_rows) <= 250000
    AND (SELECT count(*) FROM sample_rows) <= 250000 AS complete
)
SELECT jsonb_build_object(
 'complete',(SELECT complete FROM scope),
 'pass',COALESCE(SUM(observation_count) FILTER (WHERE result='PASS'),0),
 'fail',COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL'),0),
 'publishedSamples',(SELECT count(*) FROM sample_rows WHERE status='PUBLISHED'),
 'pgxParseConfigPass',COALESCE(SUM(observation_count) FILTER (
   WHERE purl='pkg:golang/github.com/jackc/pgx/v5@v5.10.0' AND symbol='ParseConfig' AND result='PASS'),0),
 'pgxParseConfigFail',COALESCE(SUM(observation_count) FILTER (
   WHERE purl='pkg:golang/github.com/jackc/pgx/v5@v5.10.0' AND symbol='ParseConfig' AND result='FAIL'),0),
 'qualityComplete',COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='complete'),0),
 'qualityPartial',COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='partial'),0),
 'qualityMissing',COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='missing'),0),
 'qualityLegacy',COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='legacy-evidence-incomplete'),0))
FROM source_rows
"""

IDENTITY_SQL = """
SELECT jsonb_build_object(
 'backend',pg_backend_pid(),'snapshot',txid_current_snapshot()::text,
 'readOnly',current_setting('transaction_read_only')='on',
 'repeatableRead',current_setting('transaction_isolation')='repeatable read')
"""

PRIMARY_KEY_SQL = """
SELECT jsonb_build_object('indexed',
 EXISTS(SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
        JOIN pg_am a ON a.oid=c.relam
        JOIN pg_attribute p ON p.attrelid=i.indrelid AND p.attnum=i.indkey[0]
        WHERE i.indrelid='failure_clusters'::regclass
          AND i.indisprimary AND i.indisvalid AND i.indisready
          AND i.indnkeyatts=1 AND a.amname='btree' AND p.attname='id'),
 'modern',2=(SELECT count(*) FROM pg_attribute
            WHERE attrelid='failure_clusters'::regclass
              AND attname IN ('termination_kind','error_summary') AND NOT attisdropped))
"""

class Unmeasured(Exception):
    pass

class BudgetExceeded(Unmeasured):
    pass

def page_sql(cursor, modern):
    # standard_conforming_strings is set for this transaction; IDs never enter
    # the shell or a command argument. Unknown SQL literals retain the PK type.
    keyset = "" if cursor is None else "WHERE id > '" + cursor.replace("'", "''") + "'"
    expression = ("evidence_quality IN ('complete','partial') AND termination_kind <> '' AND error_summary <> ''"
                  if modern else "false")
    return (PAGE_SQL.replace("__KEYSET__", keyset)
            .replace("__PAGE_ROWS__", str(PAGE_ROWS))
            .replace("__MODERN_EXPRESSION__", expression))

def integer(value, nonnegative=False):
    if not isinstance(value, int) or isinstance(value, bool) or (nonnegative and value < 0):
        raise Unmeasured("invalid numeric proof")
    return value

def walk(query, mode):
    """Accumulate only exact pages from the one snapshot owned by the caller."""
    index = query(PRIMARY_KEY_SQL)
    if index.get("indexed") is not True:
        raise Unmeasured("existing PK index unavailable")
    modern = mode == "extended" and index.get("modern") is True
    total = dict(rows=0, currentRows=0, observations=0, unbalanced=0, modern=0,
                 pages=0, maxPageRows=0, sourceRows=0, fail=None)
    cursor = None
    while True:
        page = query(page_sql(cursor, modern))
        rows = integer(page.get("rows"), True)
        if rows > PAGE_ROWS or page.get("withinJsonBudget") is not True:
            raise BudgetExceeded("page or JSON budget exceeded")
        total["pages"] += 1
        total["maxPageRows"] = max(total["maxPageRows"], rows)
        for name in ("rows", "currentRows", "observations", "unbalanced", "modern"):
            total[name] += integer(page.get(name), name != "observations")
        if rows < PAGE_ROWS:
            break
        next_cursor = page.get("cursor")
        if not isinstance(next_cursor, str) or next_cursor == cursor:
            raise Unmeasured("cursor did not advance")
        cursor = next_cursor
    if mode == "settled" and total["currentRows"] == 0:
        source = query(SOURCE_SQL)
        total["sourceRows"] = integer(source.get("rows"), True)
        if total["sourceRows"] > SOURCE_ROWS:
            raise BudgetExceeded("empty-ledger source budget exceeded")
        total["fail"] = integer(source.get("fail"))
    if mode == "extended":
        detail = query(DETAIL_SQL)
        if detail.get("complete") is not True:
            raise BudgetExceeded("source or sample census budget exceeded")
        for name in ("pass", "fail", "publishedSamples", "pgxParseConfigPass", "pgxParseConfigFail",
                     "qualityComplete", "qualityPartial", "qualityMissing", "qualityLegacy"):
            integer(detail.get(name))
        total["detail"] = detail
        total["modernAvailable"] = modern
    return total

class Session:
    def __init__(self):
        self.started = time.monotonic()
        self.deadline = self.started + SQL_SECONDS
        self.process = None
        self.selector = selectors.DefaultSelector()
        self.buffer = bytearray()
        try:
            self.process = subprocess.Popen(
                ["docker","compose","exec","-T","-e",
                 "PGOPTIONS=-c statement_timeout=20000 -c idle_in_transaction_session_timeout=20000",
                 "db","psql","-X","-qAt","-v","ON_ERROR_STOP=1","-U","csx","-d","csx"],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=0)
            os.set_blocking(self.process.stdin.fileno(), False)
            os.set_blocking(self.process.stdout.fileno(), False)
            self.selector.register(self.process.stdout, selectors.EVENT_READ)
            self.send("BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;\nSET LOCAL standard_conforming_strings=on;")
            self.identity = self.query(IDENTITY_SQL)
            if self.identity.get("readOnly") is not True or self.identity.get("repeatableRead") is not True:
                raise Unmeasured("snapshot transaction unavailable")
        except BaseException:
            self.close()
            raise

    def remaining(self):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise Unmeasured("cumulative SQL budget exhausted")
        return remaining

    def send(self, sql):
        data = (sql + "\n").encode("utf-8")
        with selectors.DefaultSelector() as writer:
            writer.register(self.process.stdin, selectors.EVENT_WRITE)
            while data:
                if not writer.select(self.remaining()):
                    raise Unmeasured("bounded write unavailable")
                try:
                    count = os.write(self.process.stdin.fileno(), data)
                except BlockingIOError:
                    continue
                if count <= 0:
                    raise Unmeasured("bounded write unavailable")
                data = data[count:]

    def query(self, sql):
        self.send("SET LOCAL statement_timeout=" + str(max(1, min(20000, int(self.remaining() * 1000)))) + ";\n" + sql + ";")
        while True:
            remaining = self.remaining()
            newline = self.buffer.find(b"\n")
            if newline >= 0:
                line = bytes(self.buffer[:newline])
                del self.buffer[:newline+1]
                break
            if len(self.buffer) > 8192 or not self.selector.select(remaining):
                raise Unmeasured("bounded result unavailable")
            try:
                part = os.read(self.process.stdout.fileno(), 8193)
            except BlockingIOError:
                continue
            if not part:
                raise Unmeasured("bounded result unavailable")
            self.buffer.extend(part)
        if len(line) > 8192:
            raise Unmeasured("bounded result unavailable")
        try:
            result = json.loads(line)
        except (ValueError, UnicodeError):
            raise Unmeasured("bounded result malformed") from None
        if not isinstance(result, dict):
            raise Unmeasured("bounded result malformed")
        return result

    def finish(self):
        if self.query(IDENTITY_SQL) != self.identity:
            raise Unmeasured("snapshot changed")
        self.send("COMMIT;")
        self.process.stdin.close()
        if self.process.wait(timeout=self.remaining()) != 0:
            raise Unmeasured("read-only transaction did not finish")

    def close(self):
        self.selector.close()
        if self.process is not None:
            if self.process.poll() is None:
                self.process.kill()
                self.process.wait(timeout=5)
            for pipe in (self.process.stdin, self.process.stdout):
                if pipe is not None and not pipe.closed:
                    pipe.close()

def collect(mode):
    session = None
    try:
        session = Session()
        proof = walk(session.query, mode)
        session.finish()
        return proof
    finally:
        if session is not None:
            session.close()

def emit(mode, proof):
    if mode == "settled":
        fields = ["complete",proof["rows"],proof["sourceRows"],proof["fail"],
                  proof["observations"],proof["unbalanced"],proof["pages"],proof["maxPageRows"],"true","true"]
        print("|".join("" if x is None else str(x) for x in fields))
        return
    detail = proof["detail"]
    counts = {k: detail[k] for k in ("pass","fail","publishedSamples","pgxParseConfigPass","pgxParseConfigFail")}
    counts.update(failureClusterObservations=proof["observations"],unbalancedFailureClusterRows=proof["unbalanced"])
    quality = {"available":False}
    if proof["modernAvailable"]:
        quality = dict(available=True,fail=detail["fail"],complete=detail["qualityComplete"],
                       partial=detail["qualityPartial"],missing=detail["qualityMissing"],
                       legacyEvidenceIncomplete=detail["qualityLegacy"])
        quality["balanced"] = quality["fail"] == sum(quality[k] for k in ("complete","partial","missing","legacyEvidenceIncomplete"))
    print("invariants=" + json.dumps(counts,separators=(",",":")))
    print("modern_failure_clusters=" + str(proof["modern"]))
    print("failure_evidence_quality=" + json.dumps(quality,separators=(",",":")))
    print("failure_ledger_rows_examined=" + str(proof["rows"]))
    print("failure_ledger_pages_examined=" + str(proof["pages"]))
    print("failure_ledger_page_row_limit=" + str(PAGE_ROWS))
    print("failure_ledger_snapshot_complete=true")

def main():
    mode = sys.argv[1] if len(sys.argv) == 2 else ""
    if mode not in ("settled","extended"):
        raise SystemExit(2)
    try:
        emit(mode, collect(mode))
    except (OSError, ValueError, Unmeasured, subprocess.SubprocessError):
        # No cursor, SQL error or partially accumulated total is published.
        if mode == "settled":
            print("unavailable||||||||false|false")
        else:
            print("detail_budget_status=collection-budget-exceeded")
        raise SystemExit(3)

if __name__ == "__main__":
    main()
