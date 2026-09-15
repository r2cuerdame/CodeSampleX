import json,sys,time,urllib.request,urllib.parse,collections
from concurrent.futures import ThreadPoolExecutor
W=sys.argv[1]
P=json.load(open(W+"/pkgversions.json"))
UA={"User-Agent":"codesamplex-audit-readonly/1.0 (offline corpus audit)"}
def get(url,timeout=20):
    req=urllib.request.Request(url,headers=UA)
    try:
        with urllib.request.urlopen(req,timeout=timeout) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        return e.code, None
    except Exception as e:
        return -1, str(e)

npm=[r for r in P if r["eco"]=="npm"]
names=sorted({r["name"] for r in npm})
print("fetching",len(names),"npm packuments (abbreviated)...",flush=True)
res={}
def one(n):
    # abbreviated packument: versions + dist-tags, far smaller than full
    url="https://registry.npmjs.org/"+urllib.parse.quote(n,safe='@')
    req=urllib.request.Request(url,headers={**UA,"Accept":"application/vnd.npm.install-v1+json"})
    for attempt in range(3):
        try:
            with urllib.request.urlopen(req,timeout=25) as r:
                d=json.load(r)
                return n,{"ok":True,"versions":sorted(d.get("versions",{}).keys()),
                          "latest":(d.get("dist-tags") or {}).get("latest")}
        except urllib.error.HTTPError as e:
            if e.code==404: return n,{"ok":False,"code":404}
            time.sleep(1+attempt)
        except Exception:
            time.sleep(1+attempt)
    return n,{"ok":False,"code":-1}
with ThreadPoolExecutor(max_workers=12) as ex:
    for i,(n,d) in enumerate(ex.map(one,names)):
        res[n]=d
        if i%100==0: print("  ",i,flush=True)
json.dump(res,open(W+"/npm_registry.json","w"))
print("done. failures:",sum(1 for d in res.values() if not d.get("ok")))
