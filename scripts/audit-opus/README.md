# audit-opus diagnostic helpers

Throwaway scripts written for the 2026-09-15 Opus audit (`AUDIT_OPUS.md`). They are kept
so every number in that report can be recomputed rather than taken on trust. They are not
part of the product build and nothing imports them.

All of them are **read-only**: they consume a local dump, or they issue `GET` requests.
None writes to production.

## Expected working directory

Each script takes the directory holding the dumps as `argv[1]`. Produce the dumps first
with the `COPY ... TO STDOUT` commands in `AUDIT_OPUS_COMMANDS.md` section 8, then:

```
<workdir>/samples.b64            raw dump, one base64 JSON object per line
<workdir>/receipts.b64           raw dump
<workdir>/sample_packages.txt    "<sampleId> <purl>" per line
<workdir>/demand.txt             "<purl> <obs> <symbols> <fails>" per line (section 12)
```

## Order

| Script | Produces | Answers |
|---|---|---|
| `decode.py <in.b64> <out.json>` | `samples.json`, `receipts.json` | decodes the dumps |
| `analyze1.py <workdir>` | stdout | corpus totals, goal quality (D1), contract quality, symbol presence (D2), ecosystem spread |
| `analyze2.py <workdir>` | stdout | duplicates (D3), saturation, per-package concentration |
| `extract_pkgs.py <workdir>` | `pkgversions.json` | distinct `(eco, name, version)` coordinates |
| `checkreg.py <workdir>` | `npm_registry.json` | fetches 936 abbreviated npm packuments |
| `verver.py <workdir>` | stdout, `npm_missing_versions.json` | D6 — does every pinned npm version exist |
| `analyze3.py <workdir>` | stdout, `symbol_mismatch.json` | staleness vs `dist-tags.latest`; symbol/package prefix coherence |
| `analyze4.py <workdir>` | stdout, `builtin_symbols.json` | builtin-as-symbol check, spelling split, receipt `resolvedPackages` linkage |
| `analyze5.py <workdir>` | stdout | D4 — purl canonicalization and split spellings |
| `analyze6.py <workdir>` | stdout | D5 — Go stdlib as a module; index vs manifest disagreement |
| `analyze7.py <workdir>` | stdout | duplicate recency vs the 2026-08-19 dedup pass; placeholder trend by month |
| `coverage.py <workdir>` | `coverage.json` | coverage priority, shim classification, oversaturation |
| `build_json.py <workdir> <out>` | `AUDIT_OPUS_DATA.json` | assembles the corpus half of the machine-readable report |
| `add_perf.py <out>` | updates the same file | adds the performance half |

## Probes

| Script | Use |
|---|---|
| `probe.sh <url> [n]` | n sequential `GET`s; prints the status distribution and min/median/p90/max latency |
| `cachetest.sh` | hammers one uncached package page until it returns 200, then measures the follow-ups that should be cache hits (P0-1) |

`probe.sh` and `cachetest.sh` issue real traffic to the public site. Both are bounded to
tens of requests; do not loop them.

## Caveat

`analyze3.py`'s symbol/package prefix check is a **rejected** heuristic, kept for
transparency. It flags 20.7% of symbol-bearing samples, and inspection showed the large
majority are legitimate bare spellings (`safeParse` for zod, `errgroup.WithContext` for
`golang.org/x/sync`) that `schema.md` explicitly supports via `symbolSpellings`. No
symbol-accuracy defect is claimed in `AUDIT_OPUS.md` on the strength of it. `analyze4.py`
replaces it with the narrower builtin check.
