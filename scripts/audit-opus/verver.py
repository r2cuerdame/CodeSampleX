import json,sys,collections
W=sys.argv[1]
P=json.load(open(W+"/pkgversions.json"))
REG=json.load(open(W+"/npm_registry.json"))
npm=[r for r in P if r["eco"]=="npm"]
missing=[];ok=[];
for r in npm:
    d=REG.get(r["name"])
    if not d or not d.get("ok"): continue
    if r["version"] in d["versions"]: ok.append(r)
    else: missing.append({**r,"latest":d.get("latest"),"nversions":len(d["versions"])})
print("=== npm version existence check ===")
print("npm coordinates checked :",len(npm))
print("version EXISTS on registry:",len(ok))
print("version ABSENT on registry:",len(missing), f"({100*len(missing)/len(npm):.1f}%)")
print("live samples affected     :",sum(m["samples"] for m in missing))
print()
print("Top absent coordinates by sample count:")
for m in sorted(missing,key=lambda x:-x["samples"])[:40]:
    print(f"  {m['samples']:4d}  {m['name']}@{m['version']}   (registry latest={m['latest']})")
json.dump(missing,open(W+"/npm_missing_versions.json","w"),indent=1)
