import json,collections,re,sys,urllib.parse
W=sys.argv[1]
S=json.load(open(W+"/samples.json",encoding='utf-8'))
live=[x for x in S if not x['quarantined']]
rows=collections.Counter()
for x in live:
    for p in (x['manifest'].get('packages') or []):
        m=re.match(r'pkg:([^/]+)/(.+)@([^@]+)$',p)
        if m: rows[(m.group(1), urllib.parse.unquote(m.group(2)), m.group(3))]+=1
out=[{"eco":e,"name":n,"version":v,"samples":c} for (e,n,v),c in rows.items()]
json.dump(out,open(W+"/pkgversions.json","w"))
print("distinct (eco,name,version):",len(out))
print(collections.Counter(r["eco"] for r in out))
print("distinct npm names:",len({r["name"] for r in out if r["eco"]=="npm"}))
print("distinct pypi names:",len({r["name"] for r in out if r["eco"]=="pypi"}))
