#!/usr/bin/env python3
"""Read-only version-currency check of live corpus purls against public registries.
Input: live_purls.tsv (purl<TAB>samples). Output: version_check.json with latest version per package
and whether the sampled version is the latest / same major / older major / unknown.
Only public package names leave this machine (registry GET requests)."""
import json, re, sys, urllib.parse, urllib.request, concurrent.futures, time
from collections import defaultdict
inp, out = sys.argv[1], sys.argv[2]
rows = [l.rstrip("\n").split("\t") for l in open(inp, encoding="utf-8") if l.startswith("pkg:")]
pkgs = defaultdict(list)
for purl, n in rows:
    m = re.match(r'^pkg:([^/]+)/(.+)@([^@]+)$', purl)
    if not m: continue
    eco, name, ver = m.group(1), urllib.parse.unquote(m.group(2)), m.group(3)
    pkgs[(eco, name)].append((ver, int(n), purl))
def get(url, accept=None):
    req = urllib.request.Request(url, headers={"User-Agent": "csx-audit/1 (read-only)", **({"Accept": accept} if accept else {})})
    with urllib.request.urlopen(req, timeout=20) as r:
        return json.loads(r.read().decode("utf-8", "replace"))
def latest(eco, name):
    try:
        if eco == "npm":
            d = get("https://registry.npmjs.org/" + urllib.parse.quote(name, safe="@"), "application/vnd.npm.install-v1+json")
            return d.get("dist-tags", {}).get("latest")
        if eco == "pypi":
            return get(f"https://pypi.org/pypi/{name}/json")["info"]["version"]
        if eco == "cargo":
            d = get(f"https://crates.io/api/v1/crates/{name}")
            return d["crate"].get("max_stable_version") or d["crate"].get("max_version")
        if eco == "golang":
            esc = re.sub(r'[A-Z]', lambda c: '!' + c.group(0).lower(), name)
            return get(f"https://proxy.golang.org/{esc}/@latest")["Version"]
        if eco == "gem":
            return get(f"https://rubygems.org/api/v1/versions/{name}/latest.json")["version"]
        if eco == "hex":
            d = get(f"https://hex.pm/api/packages/{name}")
            return d.get("latest_stable_version") or d.get("latest_version")
        if eco == "pub":
            return get(f"https://pub.dev/api/packages/{name}")["latest"]["version"]
        if eco == "composer":
            d = get(f"https://repo.packagist.org/p2/{name}.json")
            vs = [v["version"] for v in d["packages"][name] if not re.search(r'(dev|alpha|beta|rc)', v["version"], re.I)]
            return vs[0] if vs else None
        if eco == "maven":
            g, a = name.split("/", 1)
            d = get(f"https://search.maven.org/solrsearch/select?q=g:{g}+AND+a:{a}&rows=1&wt=json")
            docs = d["response"]["docs"]
            return docs[0]["latestVersion"] if docs else None
    except Exception as e:
        return f"ERR:{type(e).__name__}"
    return None
def parse(v):
    v = v.lstrip("v")
    m = re.match(r'^(\d+)(?:\.(\d+))?(?:\.(\d+))?', v)
    return tuple(int(x or 0) for x in m.groups()) if m else None
def classify(ver, lat):
    if not lat or str(lat).startswith("ERR"): return "unknown"
    a, b = parse(ver), parse(str(lat))
    if not a or not b: return "unknown"
    if ver.lstrip("v") == str(lat).lstrip("v"): return "latest"
    if a[0] == b[0]: return "same_major_older" if a < b else "same_major_newer_or_pre"
    return "older_major" if a < b else "newer_than_latest"
results = []
keys = list(pkgs)
t0 = time.time()
with concurrent.futures.ThreadPoolExecutor(max_workers=16) as ex:
    for (eco, name), lat in zip(keys, ex.map(lambda k: latest(*k), keys)):
        for ver, n, purl in pkgs[(eco, name)]:
            results.append({"purl": purl, "ecosystem": eco, "name": name, "version": ver, "samples": n, "latest": lat, "class": classify(ver, lat)})
json.dump({"checkedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "elapsedSeconds": round(time.time()-t0,1), "rows": results}, open(out, "w", encoding="utf-8"), indent=1)
summ = defaultdict(lambda: defaultdict(int))
for r in results: summ[r["ecosystem"]][r["class"]] += r["samples"]
print(json.dumps(summ, indent=1))
