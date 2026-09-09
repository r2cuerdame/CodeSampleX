#!/usr/bin/env python3
"""
PostgreSQL Slow-Query Diagnostics & Code Attribution Tool for CodeSampleX.

Queries pg_stat_statements, attributes each normalized SQL statement to its
corresponding Go store method in internal/serverstore/ and HTTP routes, and
reports execution metrics (total time, mean latency, max spike, IO wait,
cache hit percentage, temp block spill).

Privacy note: Queries in pg_stat_statements are pre-parameterized ($1, $2) by
PostgreSQL. No literals or secret values are captured or emitted.
"""

import json
import subprocess
import sys
import re

QUERY_CATALOG = [
    {
        "pattern": r"failure_clusters.*WHERE.*package_name\s*=\s*\$1",
        "method": "ListFailureClusters / ListFailureClustersIncludingPreserved",
        "file": "internal/serverstore/pg.go:2807",
        "route": "/golang/{pkg...}, /npm/{pkg...}, /python/{pkg...} (package/symbol pages), search",
        "class": "interactive / background",
    },
    {
        "pattern": r"WITH wanted_key AS MATERIALIZED",
        "method": "ListWanted",
        "file": "internal/serverstore/pg.go:3373",
        "route": "/wanted, /v1/wanted, package pages, /admin farm panel",
        "class": "interactive / background",
    },
    {
        "pattern": r"compatibility_snapshots.*WHERE.*purl\s*=\s*\$1",
        "method": "GetSnapshotsForPURL",
        "file": "internal/serverstore/pg.go:560",
        "route": "/golang/{pkg...} (package detail/symbol pages)",
        "class": "interactive",
    },
    {
        "pattern": r"SELECT DISTINCT purl, symbol FROM evidence_agg",
        "method": "ListSnapshotTargets",
        "file": "internal/serverstore/pg.go:742",
        "route": "Compatibility builder aggregation loop (SnapshotTargets)",
        "class": "background",
    },
    {
        "pattern": r"SELECT sample_id.*FROM samples.*WHERE NOT quarantined.*ORDER BY created_at DESC",
        "method": "ListSamples / ListSamplesPage",
        "file": "internal/serverstore/pg.go:910",
        "route": "/samples (web), /v1/samples (API), builder sample iteration",
        "class": "interactive / background",
    },
    {
        "pattern": r"FROM samples s JOIN receipts r ON r\.sample_id\s*=\s*s\.sample_id.*WHERE NOT s\.quarantined",
        "method": "ListSnapshotTargets (receipt claims validation)",
        "file": "internal/serverstore/pg.go:764",
        "route": "Compatibility builder aggregation loop",
        "class": "background",
    },
    {
        "pattern": r"SELECT ecosystem, child_name, child_version.*FROM dependency_edge",
        "method": "ListDependencies / SearchDependencies",
        "file": "internal/serverstore/dependencyclosure_pg.go:120",
        "route": "/admin dependency tab, /v1/dependencies",
        "class": "background",
    },
    {
        "pattern": r"INSERT INTO evidence_agg",
        "method": "RecordObservation",
        "file": "internal/serverstore/pg.go:347",
        "route": "/v1/evidence/batches (evidence ingest)",
        "class": "background",
    },
    {
        "pattern": r"WITH verified_samples AS MATERIALIZED",
        "method": "AuthoringBacklog / CandidateJobs (authoringCoverageCTE)",
        "file": "internal/serverstore/dependencyclosure_pg.go:16",
        "route": "/admin farm panel, background authoring scheduler",
        "class": "background",
    },
    {
        "pattern": r"WITH pub AS.*jsonb_array_elements_text",
        "method": "AdminInsights",
        "file": "internal/serverstore/admin_insights.go:32",
        "route": "/admin insights tab",
        "class": "background",
    },
    {
        "pattern": r"sample_packages.*package\.coord\s*=\s*ANY\(\$1\)",
        "method": "SamplesForPackageCoords",
        "file": "internal/serverstore/pg.go:622",
        "route": "/golang/{pkg...}, symbol detail pages",
        "class": "interactive",
    },
    {
        "pattern": r"UPDATE evidence_agg SET.*observation_count",
        "method": "RecordObservation (update path)",
        "file": "internal/serverstore/pg.go:390",
        "route": "/v1/evidence/batches",
        "class": "background",
    },
    {
        "pattern": r"SELECT parent_name, parent_version.*FROM dependency_edge.*WHERE ecosystem\s*=\s*\$1",
        "method": "DependenciesForPackage",
        "file": "internal/serverstore/dependencyclosure_pg.go:210",
        "route": "Package detail dependencies view",
        "class": "interactive",
    },
    {
        "pattern": r"WITH candidate AS.*unnest.*ordinal FROM candidate",
        "method": "FilterSupportedJobs / FilterCandidateJobs",
        "file": "internal/serverstore/pg.go:3150",
        "route": "Farm worker job dispatch",
        "class": "background",
    },
    {
        "pattern": r"SELECT receipt_id.*FROM receipts.*WHERE sample_id\s*=\s*ANY\(\$1",
        "method": "ReceiptsForSamples",
        "file": "internal/serverstore/pg.go:1200",
        "route": "/samples, /golang/{pkg...}, /admin",
        "class": "interactive / background",
    },
    {
        "pattern": r"SELECT purl, SUM\(observation_count\)\s+AS n\s+FROM evidence_agg",
        "method": "HotPackages",
        "file": "internal/serverstore/pg.go:2441",
        "route": "Homepage (/), stats, sitemap warming",
        "class": "interactive / background",
    },
    {
        "pattern": r"compatibility_snapshots s\s+LEFT JOIN LATERAL jsonb_array_elements",
        "method": "SnapshotAges / SnapshotFreshness",
        "file": "internal/serverstore/pg.go:714",
        "route": "Compatibility builder aggregation loop",
        "class": "background",
    },
    {
        "pattern": r"WITH observed AS.*ran AS.*measured AS.*proven AS",
        "method": "FarmCoverage",
        "file": "internal/serverstore/farm_pg.go:197",
        "route": "/admin farm panel, matrix coverage scheduler",
        "class": "background",
    },
]

def attribute_query(query_text):
    for entry in QUERY_CATALOG:
        if re.search(entry["pattern"], query_text, re.IGNORECASE | re.DOTALL):
            return entry["method"], entry["file"], entry["route"], entry["class"]
    return "Unknown / uncataloged", "internal/serverstore", "N/A", "N/A"

def fetch_queries_local(limit=20, sort="total_exec_time"):
    sql = f"""
    SELECT json_agg(t) FROM (
      SELECT
        queryid,
        calls,
        round(total_exec_time::numeric, 2) AS total_ms,
        round(mean_exec_time::numeric, 2) AS mean_ms,
        round(max_exec_time::numeric, 2) AS max_ms,
        round(stddev_exec_time::numeric, 2) AS stddev_ms,
        round((shared_blk_read_time + shared_blk_write_time)::numeric, 2) AS io_ms,
        shared_blks_hit,
        shared_blks_read,
        round(shared_blks_hit::numeric / nullif(shared_blks_hit + shared_blks_read, 0) * 100, 1) AS hit_pct,
        temp_blks_read + temp_blks_written AS temp_blks,
        rows,
        query
      FROM pg_stat_statements
      ORDER BY {sort} DESC
      LIMIT {limit}
    ) t;
    """
    cmd = ["docker", "compose", "-f", "/opt/codesamplex/deploy/docker-compose.yml",
           "exec", "-T", "db", "psql", "-U", "csx", "-d", "csx", "-Atqc", sql]
    proc = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if proc.returncode != 0:
        raise RuntimeError(f"Failed to query database: {proc.stderr}")
    raw = proc.stdout.strip()
    return json.loads(raw) if raw else []

def main():
    sort_key = sys.argv[1] if len(sys.argv) > 1 else "total_exec_time"
    limit = int(sys.argv[2]) if len(sys.argv) > 2 else 15
    json_mode = "--json" in sys.argv

    try:
        data = fetch_queries_local(limit, sort_key)
    except Exception as e:
        print(f"Error fetching queries: {e}", file=sys.stderr)
        sys.exit(1)

    enriched = []
    for q in data:
        method, source_file, route, qclass = attribute_query(q["query"])
        item = {
            **q,
            "attributed_method": method,
            "source_file": source_file,
            "route": route,
            "class": qclass,
        }
        enriched.append(item)

    if json_mode:
        print(json.dumps(enriched, indent=2))
        return

    print(f"\n=== PostgreSQL Top Slow Queries (Sorted by {sort_key}, Limit {limit}) ===\n")
    print("| Rank | Calls | Total (ms) | Mean (ms) | Max (ms) | IO (ms) | Hit % | Attributed Store Method | Route / Class |")
    print("|---|---|---|---|---|---|---|---|---|")
    for i, item in enumerate(enriched):
        print(f"| {i+1} | {item['calls']} | {item['total_ms']} | {item['mean_ms']} | {item['max_ms']} | {item['io_ms']} | {item['hit_pct']}% | `{item['attributed_method']}` | {item['class']} |")

    print("\n=== Detailed Query Breakdown ===\n")
    for i, item in enumerate(enriched):
        clean_query = re.sub(r"\s+", " ", item["query"]).strip()
        print(f"### #{i+1}: {item['attributed_method']}")
        print(f"- **File:** `{item['source_file']}`")
        print(f"- **Route:** {item['route']}")
        print(f"- **Class:** `{item['class']}` | **QueryID:** `{item['queryid']}`")
        print(f"- **Metrics:** Calls={item['calls']} | Total={item['total_ms']}ms | Mean={item['mean_ms']}ms | Max={item['max_ms']}ms | IO={item['io_ms']}ms | Hit={item['hit_pct']}% | TempBlks={item['temp_blks']}")
        print(f"- **SQL Preview:** `{clean_query[:250]}...`")
        print()

if __name__ == "__main__":
    main()
