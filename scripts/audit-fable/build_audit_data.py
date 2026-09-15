#!/usr/bin/env python3
"""Assemble AUDIT_FABLE_DATA.json from the read-only scan outputs.

Inputs (all produced by the commands in AUDIT_FABLE_COMMANDS.md):
  defects.jsonl        - one JSON object per defect candidate / class row (prod-rosql.sh exports)
  version_check.json   - registry currency check (version_currency_check.py)

Only public coordinates, counts and sample content addresses are included.
The population / performance constants below were measured on 2026-09-15 and
are reproduced by the commands in AUDIT_FABLE_COMMANDS.md.
"""
import collections
import json
import sys
import time

defects_path, version_path, out_path = sys.argv[1:4]

rows = []
for line in open(defects_path, encoding="utf-8"):
    line = line.strip()
    if line.startswith("{"):
        try:
            rows.append(json.loads(line))
        except json.JSONDecodeError:
            pass
by = collections.defaultdict(list)
for r in rows:
    by[r["class"]].append(r)

vc = json.load(open(version_path, encoding="utf-8"))
vrows = vc["rows"]
vsum = collections.defaultdict(collections.Counter)
for r in vrows:
    vsum[r["ecosystem"]][r["class"]] += r["samples"]
older_major = sorted([r for r in vrows if r["class"] == "older_major"], key=lambda r: -r["samples"])


def n(cls):
    return sum(r.get("count", 1) for r in by.get(cls, []))


population = {
    "samplesTotal": 8345, "samplesLive": 7069, "samplesQuarantined": 1276,
    "receipts": 10965, "receiptsPass": 10466, "receiptsFail": 272, "receiptsSkipped": 227,
    "livePurls": 2278, "livePackageNames": 1375, "contractLines": 39580,
    "compatibilitySnapshots": 21686, "evidenceAggRows": 318688, "failureClusterRows": 236167,
    "dependencyEdges": 109686,
    "asOf": "2026-09-15T14:30Z",
    "source": "production PostgreSQL csx (read-only transaction), see AUDIT_FABLE_COMMANDS.md",
}

defect_classes = {
    "cross_pass_single_peer": {"severity": "P0", "category": "invalid_verification_linkage", "samples": n("cross_pass_single_peer"),
        "note": "status=CROSS_PASS but only ONE distinct peer ever signed a PASS receipt; the author's LOCAL_PASS is unsigned and unstored (pg.go draft promotion)."},
    "pass_receipt_without_verifier_image_only": {"severity": "P1", "category": "invalid_verification_linkage", "samples": n("pass_receipt_without_verifier_image_only"),
        "note": "Every PASS receipt for the sample lacks verifierImage: not re-runnable by digest; includes all gem/composer/hex/pub/maven samples."},
    "newest_receipt_fail": {"severity": "P1", "category": "non_runnable_or_regressed", "samples": n("newest_receipt_fail"),
        "note": "Live sample whose most recent receipt is FAIL; status never downgrades (statusRank only upgrades)."},
    "duplicate_purl_symbolset": {"severity": "P1", "category": "duplicate", "groups": len(by["duplicate_purl_symbolset"]), "samples": n("duplicate_purl_symbolset"),
        "note": "Live samples sharing identical manifest.packages AND manifest.symbols; the 2026-08-19 dedup quarantined 983 but these survived or were re-issued afterwards."},
    "duplicate_contract": {"severity": "P1", "category": "duplicate", "groups": len(by["duplicate_contract"]), "samples": n("duplicate_contract"),
        "note": "Live samples whose case.contract array is byte-identical to another live sample for the same subject."},
    "shared_case_id": {"severity": "P2", "category": "duplicate", "groups": len(by["shared_case_id"]), "samples": n("shared_case_id")},
    "raw_scoped_npm_purl": {"severity": "P1", "category": "package_symbol_mismatch", "samples": n("raw_scoped_npm_purl"),
        "note": "manifest.packages uses pkg:npm/@scope/... instead of pkg:npm/%40scope/...; receipt resolvedPackages use %40, so containment checks and coord joins miss."},
    "golang_version_without_v": {"severity": "P1", "category": "package_symbol_mismatch", "samples": n("golang_version_without_v"),
        "note": "manifest.packages golang purl without the v prefix while receipts resolve v-prefixed; 75 samples have PASS receipts whose resolvedPackages do not contain the manifest purl."},
    "golang_stdlib_as_package": {"severity": "P2", "category": "package_symbol_mismatch", "samples": n("golang_stdlib_as_package"),
        "note": "Go standard library paths recorded as packages with two version spellings (1.26.5 vs go1.26.5); registry lookups cannot resolve them."},
    "golang_bare_symbol_spelling": {"severity": "P2", "category": "package_symbol_mismatch", "samples": n("golang_bare_symbol_spelling"),
        "note": "Golang symbols spelled as alias.Name (1379 symbols) vs import/path.Name (1788); wanted/coverage joins compare exact strings."},
    "template_goal": {"severity": "P1", "category": "inaccurate_description", "samples": n("template_goal"),
        "note": "case.goal is the unedited authoring template 'verify X in pkg:...'; 3505/7069 = 49.6% of live corpus."},
    "no_symbols": {"severity": "P2", "category": "inaccurate_description", "samples": n("no_symbols"),
        "note": "manifest.symbols empty: sample cannot be placed on any symbol cell."},
    "metadata_only_contract": {"severity": "P1", "category": "weak_value", "samples": n("metadata_only_contract"),
        "note": "Every contract line asserts package.json/manifest/type-declaration/binary-header facts, not runtime behaviour."},
    "contract_mentions_no_symbol": {"severity": "P2", "category": "inaccurate_description", "samples": n("contract_mentions_no_symbol"),
        "note": "No contract line mentions the last segment of any declared symbol (heuristic; expect false positives for property-style symbols)."},
    "orphan_search_hit_sample": {"severity": "P2", "category": "invalid_evidence_linkage",
        "rows": by["orphan_search_hit_sample"][0]["count"] if by["orphan_search_hit_sample"] else None,
        "note": "search_hits rows whose sample_id no longer exists in samples (hard-deleted); usage telemetry for those offers is unattributable."},
    "older_major_version": {"severity": "P2", "category": "stale_version", "purls": len(older_major), "samples": sum(r["samples"] for r in older_major),
        "note": "Sampled version is a lower MAJOR than the registry latest at check time. Not a defect per se (evidence does not decay) but flags where 'latest' coverage is absent."},
}

out = {
    "schema": "csx-audit-fable/1",
    "generatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    "population": population,
    "defectClasses": defect_classes,
    "defects": {k: v for k, v in by.items() if k not in ("saturation_density", "demand_gap_observed_no_sample")},
    "coverageValue": {
        "saturation": by["saturation_density"],
        "demandGapsObservedNoSample": by["demand_gap_observed_no_sample"],
        "searchUsage": {"hits": 6859, "distinctSamplesHit": 1989, "reporters": 100, "misses": 4150, "missReporters": 88,
                        "liveSamplesNeverHit": 5349, "liveSamplesHit1to2": 1369, "liveSamplesHit3to10": 272, "liveSamplesHitOver10": 79,
                        "window": "2026-08-21..2026-09-15"},
        "wanted": {"rows": 4680, "asks": 5636, "rowsWithAnyLiveSample": 4655,
                   "note": "wanted is >99% name-level answered: it is dominated by the farm's own authoring expansion, not external demand"},
        "versionCurrencyByEcosystem": {k: dict(v) for k, v in vsum.items()},
        "olderMajorTop": older_major[:60],
    },
    "performance": {
        "host": {"vcpu": 2, "ramMiB": 1906, "loadAvg1m_at_1426Z": 8.92, "serverContainerMemMiB": 694, "serverContainerLimitMiB": 768,
                 "goMemLimitMiB": 600, "dbContainerMemMiB": 418, "dbContainerLimitMiB": 640},
        "pgStatStatements": {
            "window": "2026-09-09T05:49Z..2026-09-15T14:10Z", "totalExecHours": 119.3, "calls": 43544908,
            "top": [
                {"queryid": "-8919612395518868614", "what": "listFailureClusters (pg.go:3093) per package", "calls": 336679, "totalSeconds": 95112, "meanMs": 283, "maxMs": 8000, "rows": 55442825, "sharedReadMs": 82083649},
                {"queryid": "5126368844612351286", "what": "builder_purl_coord() SQL function body (pg_builder_prestage.go:16)", "calls": 10358032, "totalSeconds": 65367, "meanMs": 6.31, "measuredPerRowMs": 17.4},
                {"queryid": "-1034863950460726248", "what": "upsertFailureClusterSQL (pg.go:2940)", "calls": 20025622, "totalSeconds": 41416, "meanMs": 2.07},
                {"queryid": "-1838375435808251149", "what": "authoringExpansionCandidatesSQL / authoringCoverageCTE (authoring_pg.go:298)", "calls": 192, "totalSeconds": 35220, "meanMs": 183436, "maxMs": 432955, "tempBlocksWritten": 5337760},
                {"queryid": "5754581964168977175", "what": "farmBacklogStocksSQL / authoringCoverageCTE (farmbacklog_pg.go:42)", "calls": 2107, "totalSeconds": 21950, "meanMs": 10418, "maxMs": 22855},
                {"queryid": "-4616489319632358898", "what": "INSERT compatibility_snapshots ON CONFLICT (PutSnapshots)", "calls": 2322683, "totalSeconds": 14692, "meanMs": 6.33},
                {"queryid": "-827627249904167911", "what": "evidence_agg target read (EvidenceForTarget)", "calls": 2505195, "totalSeconds": 13826, "meanMs": 5.52, "rows": 33338607},
                {"queryid": "-8292064883623834489", "what": "farm_pg.go:58 WITH pub AS (FarmCompletenessNow)", "calls": 7641, "totalSeconds": 13108, "meanMs": 1715},
                {"queryid": "-8060285611411695903", "what": "authoringCoverageCTE variant (farm coverage)", "calls": 1059, "totalSeconds": 10741, "meanMs": 10143},
                {"queryid": "2969576699904556945", "what": "GetSnapshotsForPURL (pg.go:700)", "calls": 430738, "totalSeconds": 9753, "meanMs": 22.6},
                {"queryid": "-4544060618235083690", "what": "SnapshotKeys full key scan (pg.go:795) - HotPackages every 60s", "calls": 4979, "totalSeconds": 7317, "meanMs": 1470, "maxMs": 32236, "rows": 103309393},
                {"queryid": "8649384622017691265", "what": "SnapshotUpdatedAt jsonb_array_elements over every snapshot (pg.go:838)", "calls": 517, "totalSeconds": 6063, "meanMs": 11727, "maxMs": 37922},
                {"queryid": "-1717588399536768741", "what": "HotShardKeys evidence_agg GROUP BY purl (pg.go:2705)", "calls": 624, "totalSeconds": 4728, "meanMs": 7576},
                {"queryid": "-7089594382960164887", "what": "stats refresh COUNT(DISTINCT bucket) FROM evidence_dedup (pg.go:3451)", "calls": 150, "totalSeconds": 3190, "meanMs": 21269, "maxMs": 101852},
                {"queryid": "-181493637755466483", "what": "ListSnapshotTargets receipts x samples join (pg.go:905) - /v1/registry/packages uncached", "calls": 115, "totalSeconds": 2132, "meanMs": 18541, "maxMs": 477291},
                {"queryid": "-7356693871952532290", "what": "SELECT sample_id, manifest::text FROM samples WHERE NOT quarantined (HotShardKeys, pg.go:2742)", "calls": 561, "totalSeconds": 2062, "meanMs": 3675},
            ],
        },
        "pgStatUserTables": {
            "samples": {"seqScan": 2575997, "seqTupRead": 7553489329},
            "sample_packages": {"seqScan": 3783048, "seqTupRead": 25052330376},
            "receipts": {"seqScan": 1352564, "seqTupRead": 6948551529},
            "evidence_agg": {"seqScan": 255307, "seqTupRead": 9395035302},
            "compatibility_snapshots": {"seqScan": 86780, "seqTupRead": 1203005922},
            "dependency_edge": {"seqScan": 123606, "seqTupRead": 3360502745},
        },
        "pgStatDatabase": {"cacheHitPct": 97.5, "tempFiles": 1049628, "tempBytes": "4615 GB", "xactCommit": 129838465, "xactRollback": 5450377},
        "unusedIndexes": [{"name": "samples_manifest_lower_trgm_idx", "sizeMiB": 26, "idxScan": 1}, {"name": "failure_clusters_pkey", "sizeMiB": 9.3, "idxScan": 0}],
        "poolPressureLog": {
            "window": "2026-09-15T09:39Z..14:00Z (container uptime)", "events": 1044,
            "byClassCause": {"interactive pool_busy": 878, "background query_timeout": 76, "probe pool_busy": 47, "interactive deferred_refused": 18, "interactive query_timeout": 17, "interactive admission_refused": 8},
            "byPath": {"/npm/*": 290, "/v1/shards/*": 274, "/dependencies": 212, "/admin/api/farm": 76, "/golang/*": 74, "/healthz": 47, "/pypi/*": 29},
            "maxWaitMs": {"interactive": 1362, "probe": 1838, "background": 8156},
            "productionEnv": {"CSX_DB_READ_CONNS": "2", "CSX_DB_READ_WAIT": "250ms", "CSX_SNAPSHOT_INTERVAL": "5m"},
        },
        "builderPasses_2026-09-15": [
            {"start": "09:39:38", "full": False, "total": "53.8s"}, {"start": "09:45:31", "full": False, "total": "16m48s"},
            {"start": "10:07:20", "full": False, "total": "31m43s"}, {"start": "10:44:03", "full": True, "total": "2h12m22s"},
            {"start": "13:01:25", "full": False, "total": "5m37s"}, {"start": "13:12:02", "full": False, "total": "4m24s"},
            {"start": "13:21:27", "full": False, "total": "1m04s"}, {"start": "13:27:31", "full": False, "total": "55s"},
            {"start": "13:33:26", "full": False, "total": "1m51s"}, {"start": "13:40:17", "full": False, "total": "1m13s"},
            {"start": "13:46:31", "full": False, "total": "5m04s"}, {"start": "13:56:35", "full": True, "total": ">40m at 14:27 (still running)"},
        ],
        "publicPageProbes": {
            "note": "from a Windows client, 2 rounds at 14:00-14:07Z during a full builder pass; /healthz baseline RTT 0.4-0.8s",
            "ttfbSeconds": {
                "/healthz": [0.78, 0.42], "/": [0.82, 0.63], "/compatibility": [7.05, 3.62], "/dependencies": [1.10, 0.63], "/gaps": [1.05, 0.93],
                "/findings": [1.36, 0.91], "/samples": [4.01, 2.73], "/features": [0.73, 0.78],
                "/golang/github.com%2Fjackc%2Fpgx%2Fv5": ["503", "503"], "/npm/hono": ["503", "503"], "/npm/@babel/core": ["503", "503"],
                "/golang/golang.org%2Fx%2Fsys": ["503", "503"], "/pypi/pymatting/1.1.15/samples/knn-laplacian-64824931": [0.69, 1.77],
                "/v1/stats": [0.91, 0.98], "/v1/wanted": [0.48, 0.70], "/v1/shards/npm/hono/4": [1.51, 2.70],
                "/sitemap.xml": [44.76, 0.82], "/sitemaps/packages-1.xml": [0.67, 0.87], "/sitemaps/samples-1.xml": [0.58, 0.66],
            },
            "packagePage503Persistence": "11 of 12 package-page probes between 14:26:35Z and 14:27:18Z returned 503 Retry-After:2",
        },
        "accessLog27d": {
            "totalRequests": 1981159,
            "routes": {"shards": {"requests": 1227089, "502": 21405, "503": 9057}, "verification_jobs": {"requests": 630295, "502": 1429},
                       "authoring_work": {"requests": 23374, "503": 2939}, "stats": {"requests": 4374, "503": 282}, "search_hit": {"requests": 7746, "500": 531},
                       "peers": {"requests": 3676, "500": 277}, "wanted": {"requests": 2995, "503": 112}, "registry": {"requests": 138, "503": 34}},
            "note": "privacy-safe Caddy log carries only route label + status; web HTML pages are log_skip'd and carry no duration",
        },
        "explainProbes": {"builder_purl_coord_1000_rows_ms": 17440, "plain_expression_1000_rows_ms": 1.2, "listFailureClusters_golang_x_sys_ms": 32164,
                          "listFailureClusters_golang_x_sys_rows": 9225, "listFailureClusters_golang_x_sys_heap_blocks_read": 6541,
                          "snapshotKeys_ms": 82.5, "samples_page_ms": 9.5},
    },
}
json.dump(out, open(out_path, "w", encoding="utf-8"), indent=1)
print("classes:", {k: len(v) for k, v in by.items()})
print("wrote", out_path)
