import json,sys,collections,re,urllib.parse
W=sys.argv[1]
S=json.load(open(W+"/samples.json",encoding='utf-8'))
SP=[l.split() for l in open(W+"/sample_packages.txt",encoding='utf-8') if l.strip()]
live={x['sampleId']:x for x in S if not x['quarantined']}
# purl -> live sample count
cov=collections.Counter()
for sid,purl in SP:
    if sid in live: cov[purl]+=1
def canon(p):
    m=re.match(r'pkg:([^/]+)/(.+)@([^@]+)$',p)
    if not m: return p
    e,n,v=m.groups(); n=urllib.parse.unquote(n).lower()
    if e=='golang' and not v.startswith('v'): v='v'+v
    return f"pkg:{e}/{n}@{v}"
covc=collections.Counter()
for p,c in cov.items(): covc[canon(p)]+=c

# platform-shim / non-API package filter
SHIM=re.compile(r'(^|/)(%40|@)?(esbuild|rollup|swc|next|tailwindcss|napi-rs|parcel|biomejs|oxc|unrs|lightningcss|node-rs|img)/',re.I)
PLAT=re.compile(r'(linux|darwin|win32|windows|android|freebsd|openbsd|sunos)[-_](x64|arm64|ia32|arm|riscv64|s390x|ppc64|loong64|mips64el)|[-_](musl|gnu|msvc|gnueabihf|musleabihf|eabi)$|^fsevents$',re.I)
def is_shim(name):
    short=name.split('/')[-1]
    return bool(PLAT.search(short)) or bool(PLAT.search(name))

rows=[]
for line in open(W+"/demand.txt",encoding='utf-8'):
    p=line.split()
    if len(p)!=4: continue
    purl,obs,syms,fails=p[0],int(p[1]),int(p[2]),int(p[3])
    m=re.match(r'pkg:([^/]+)/(.+)@([^@]+)$',purl)
    if not m: continue
    eco,name,ver=m.groups(); name=urllib.parse.unquote(name)
    rows.append({"purl":purl,"canon":canon(purl),"eco":eco,"name":name,"version":ver,
                 "observations":obs,"observedSymbols":syms,"failObservations":fails,
                 "liveSamples":covc.get(canon(purl),0),"platformShim":is_shim(name)})
print("demand rows:",len(rows))
real=[r for r in rows if not r["platformShim"]]
shim=[r for r in rows if r["platformShim"]]
print("platform/binary shim rows:",len(shim),f"({100*len(shim)/len(rows):.1f}%) obs={sum(r['observations'] for r in shim)}")
print("real API packages         :",len(real), f"obs={sum(r['observations'] for r in real)}")
print()
print("=== SHIMS IN THE TOP-100 DEMAND LIST ===")
top100=rows[:100]
print(f"  {sum(1 for r in top100 if r['platformShim'])}/100 of the highest-demand coordinates are platform binary shims")
print()
gap=[r for r in real if r["liveSamples"]==0]
print("=== HIGH-DEMAND, ZERO-SAMPLE (real API packages) ===")
print("count:",len(gap),"  total observations:",sum(r["observations"] for r in gap))
for r in gap[:35]:
    print(f"  obs={r['observations']:6d} sym={r['observedSymbols']:4d} fails={r['failObservations']:5d}  {r['eco']}/{r['name']}@{r['version']}")
print()
print("=== HIGH-DEMAND, THIN COVERAGE (>=1 but obs/sample > 2000) ===")
thin=[r for r in real if r["liveSamples"]>0 and r["observations"]/r["liveSamples"]>2000]
thin.sort(key=lambda r:-r["observations"])
print("count:",len(thin))
for r in thin[:20]:
    print(f"  obs={r['observations']:6d} samples={r['liveSamples']:3d} ratio={r['observations']//r['liveSamples']:6d}  {r['eco']}/{r['name']}@{r['version']}")
print()
print("=== OVERSATURATED: many samples, low demand ===")
over=[r for r in rows if r["liveSamples"]>=8 and r["observations"]<r["liveSamples"]*100]
over.sort(key=lambda r:-r["liveSamples"])
print("count:",len(over))
for r in over[:20]:
    print(f"  samples={r['liveSamples']:4d} obs={r['observations']:6d}  {r['eco']}/{r['name']}@{r['version']}")
json.dump({"gaps":gap[:300],"thin":thin[:200],"oversaturated":over[:200],
           "shimsInTop100":sum(1 for r in top100 if r['platformShim'])},
          open(W+"/coverage.json","w"),indent=1)
