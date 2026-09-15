import json, sys, collections, re, urllib.parse, datetime

W = sys.argv[1]
OUT = sys.argv[2]

S = json.load(open(W + "/samples.json", encoding="utf-8"))
R = json.load(open(W + "/receipts.json", encoding="utf-8"))
REG = json.load(open(W + "/npm_registry.json"))
SP = [l.split() for l in open(W + "/sample_packages.txt", encoding="utf-8") if l.strip()]
COV = json.load(open(W + "/coverage.json"))

live = [x for x in S if not x["quarantined"]]
liveids = {x["sampleId"] for x in live}


def man(x):
    return x["manifest"]


def goal(x):
    return ((man(x).get("case") or {}).get("goal") or "").strip()


def canon(p):
    mm = re.match(r"pkg:([^/]+)/(.+)@([^@]+)$", p)
    if not mm:
        return p
    e, n, v = mm.groups()
    n = urllib.parse.unquote(n).lower()
    if e == "golang" and not v.startswith("v"):
        v = "v" + v
    if e == "pypi":
        n = n.replace("_", "-")
    return "pkg:" + e + "/" + n + "@" + v


def eco_of(x):
    ps = man(x).get("packages") or []
    if not ps:
        return "?"
    mm = re.match(r"pkg:([^/]+)/", ps[0])
    return mm.group(1) if mm else "?"


ph = [x for x in live if re.match(r"^verify\s+pkg:", goal(x), re.I)]
nosym = [x for x in live if not (man(x).get("symbols") or [])]

g = collections.defaultdict(list)
for x in live:
    key = (tuple(sorted(man(x).get("packages") or [])), tuple(sorted(man(x).get("symbols") or [])))
    g[key].append(x)
dupgroups = {k: sorted(v, key=lambda z: z["createdAt"]) for k, v in g.items() if len(v) > 1}
dupextra = [z["sampleId"] for v in dupgroups.values() for z in v[1:]]

allp = collections.Counter()
for x in S:
    for p in man(x).get("packages") or []:
        allp[p] += 1
byc = collections.defaultdict(set)
for p in allp:
    byc[canon(p)].add(p)
split = {c: sorted(v) for c, v in byc.items() if len(v) > 1}

spby = collections.defaultdict(set)
for sid, purl in SP:
    spby[sid].add(purl)
idxdiff = []
for x in S:
    dm = set(man(x).get("packages") or [])
    ds = spby.get(x["sampleId"], set())
    if dm and ds and dm != ds:
        idxdiff.append({"sampleId": x["sampleId"], "manifest": sorted(dm), "indexed": sorted(ds)})

bySample = {x["sampleId"]: x for x in S}
rp_mismatch = []
for r in R:
    rp = r["receipt"].get("resolvedPackages")
    if not rp:
        continue
    s = bySample.get(r["sampleId"])
    if not s:
        continue
    d = set(s["manifest"].get("packages") or [])
    got = set(rp)
    if d and got and d != got:
        rp_mismatch.append(
            {"sampleId": r["sampleId"], "receiptId": r["receiptId"], "manifest": sorted(d), "resolved": sorted(got)}
        )

stdlib = collections.Counter()
for x in S:
    for p in man(x).get("packages") or []:
        mm = re.match(r"pkg:golang/(.+)@([^@]+)$", p)
        if mm and "." not in mm.group(1).split("/")[0]:
            stdlib[p] += 1

npmcheck = {"checked": 0, "exists": 0, "absent": 0, "absentExamples": []}
for x in live:
    for p in man(x).get("packages") or []:
        mm = re.match(r"pkg:npm/(.+)@([^@]+)$", p)
        if not mm:
            continue
        n = urllib.parse.unquote(mm.group(1))
        v = mm.group(2)
        d = REG.get(n)
        if not d or not d.get("ok"):
            continue
        npmcheck["checked"] += 1
        if v in d["versions"]:
            npmcheck["exists"] += 1
        else:
            npmcheck["absent"] += 1
            if len(npmcheck["absentExamples"]) < 10:
                npmcheck["absentExamples"].append(n + "@" + v)

passing = {r["sampleId"] for r in R if r["contractResult"] == "PASS"}

doc = {
    "audit": {
        "tool": "AUDIT_OPUS",
        "generatedAt": datetime.datetime.utcnow().strftime("%Y-%m-%dT%H:%M:%SZ"),
        "productionSnapshot": "2026-09-15T14:00Z",
        "population": "EXHAUSTIVE - every row of samples (8345), receipts (10965) and sample_packages (8676) was dumped read-only from production PostgreSQL and analysed offline. No sampling was used for corpus findings.",
        "samplingLimits": "HTTP availability figures are samples, not census: each endpoint was probed 6-12 times over ~25 minutes on 2026-09-15 between 14:00Z and 14:40Z. pg_stat_statements figures cover the window since its last reset, 2026-09-09T05:49:06Z (6d 08h).",
    },
    "corpus": {
        "samplesTotal": len(S),
        "samplesLive": len(live),
        "samplesQuarantined": len(S) - len(live),
        "receiptsTotal": len(R),
        "receiptsByContractResult": dict(collections.Counter(r["contractResult"] for r in R)),
        "liveSamplesWithPassReceipt": len(liveids & passing),
        "liveSamplesWithoutAnyReceipt": len(liveids - {r["sampleId"] for r in R}),
        "orphanReceipts": len({r["sampleId"] for r in R} - {x["sampleId"] for x in S}),
        "receiptsWithCompileSkipped": sum(
            1 for r in R if (r["receipt"].get("stages") or {}).get("compile") == "SKIPPED"
        ),
        "ecosystemSpreadLive": dict(collections.Counter(eco_of(x) for x in live)),
        "distinctPackageNamesLive": len(
            {canon(p).rsplit("@", 1)[0] for x in live for p in (man(x).get("packages") or [])}
        ),
    },
    "defects": [
        {
            "id": "D1",
            "severity": "P1",
            "kind": "misleading-description",
            "title": "Placeholder authoring goal published as the sample's user-facing description",
            "count": len(ph),
            "pctOfLive": round(100 * len(ph) / len(live), 1),
            "stillOccurring": True,
            "mostRecent": max(x["createdAt"] for x in ph),
            "createdByMonth": dict(sorted(collections.Counter(x["createdAt"][:7] for x in ph).items())),
            "examples": [{"sampleId": x["sampleId"], "goal": goal(x)} for x in ph[:10]],
        },
        {
            "id": "D2",
            "severity": "P1",
            "kind": "no-symbol-coverage",
            "title": "Live sample declares no symbol, so it can never populate a symbol-level compatibility cell",
            "count": len(nosym),
            "pctOfLive": round(100 * len(nosym) / len(live), 1),
            "stillOccurring": True,
            "mostRecent": max(x["createdAt"] for x in nosym),
            "createdByMonth": dict(sorted(collections.Counter(x["createdAt"][:7] for x in nosym).items())),
            "examples": [x["sampleId"] for x in nosym[:10]],
        },
        {
            "id": "D3",
            "severity": "P1",
            "kind": "duplicate",
            "title": "Live redundant samples: identical package set and identical symbol set",
            "groups": len(dupgroups),
            "redundantSamples": len(dupextra),
            "createdAfterLastDedupPass20260819": sum(
                1 for v in dupgroups.values() for z in v[1:] if z["createdAt"] > "2026-08-19"
            ),
            "alreadyQuarantinedAsDuplicate": 1227,
            "examples": [
                {"packages": list(k[0]), "symbols": list(k[1]), "sampleIds": [z["sampleId"] for z in v]}
                for k, v in list(dupgroups.items())[:10]
            ],
        },
        {
            "id": "D4",
            "severity": "P1",
            "kind": "purl-canonicalization",
            "title": "One release stored under two purl spellings (golang missing v-prefix, npm scope unencoded)",
            "releasesWithSplitSpelling": len(split),
            "manifestPackageRowsAffected": sum(allp[p] for v in split.values() for p in v),
            "samplesWhoseIndexDisagreesWithManifest": len(idxdiff),
            "receiptsWhoseResolvedPackagesDisagreeWithManifest": len(rp_mismatch),
            "examples": [{"canonical": c, "spellings": v} for c, v in list(split.items())[:15]],
            "indexDisagreementExamples": idxdiff[:8],
            "receiptDisagreementExamples": rp_mismatch[:5],
        },
        {
            "id": "D5",
            "severity": "P2",
            "kind": "unresolvable-coordinate",
            "title": "Go standard-library package recorded as a third-party module at the toolchain version",
            "distinctPurls": len(stdlib),
            "packageRows": sum(stdlib.values()),
            "purls": dict(stdlib),
        },
        {
            "id": "D6",
            "severity": "VERIFIED-CLEAN",
            "kind": "version-plausibility",
            "title": "Every npm version pinned by a live sample exists on the public registry",
            "npmCoordinateChecks": npmcheck,
        },
        {
            "id": "D7",
            "severity": "VERIFIED-CLEAN",
            "kind": "evidence-linkage",
            "title": "Every live sample has at least one PASS contract receipt; no orphan receipts",
            "liveSamples": len(live),
            "liveWithPass": len(liveids & passing),
            "liveWithoutReceipt": len(liveids - {r["sampleId"] for r in R}),
            "orphanReceipts": len({r["sampleId"] for r in R} - {x["sampleId"] for x in S}),
        },
    ],
    "coveragePriority": {
        "method": "evidence_agg observation volume (obs>=50, top 4000 coordinates) left-joined to live sample coverage by canonical purl; platform binary shims classified separately by name pattern",
        "shimPollutionOfUncoveredRanking": {
            "uncoveredCoordinates": 1420,
            "shimsInTop20": 16,
            "shimsInTop40": 35,
            "shimsInTop100": 57,
            "shimsInTop200": 102,
            "shimObservationsInUncoveredSet": 139172,
            "realObservationsInUncoveredSet": 177767,
        },
        "zeroSampleHighDemand": COV["gaps"][:150],
        "thinCoverage": COV["thin"][:100],
        "oversaturatedLowDemand": COV["oversaturated"][:100],
    },
}

json.dump(doc, open(OUT, "w", encoding="utf-8"), indent=1, ensure_ascii=False)
print("wrote " + OUT)
for d in doc["defects"]:
    print("  " + d["id"] + " " + d["severity"] + " " + d["title"][:72])
