import json,sys,collections,re,urllib.parse
W=sys.argv[1]
S=json.load(open(W+"/samples.json",encoding='utf-8'))
REG=json.load(open(W+"/npm_registry.json"))
live=[x for x in S if not x['quarantined']]
def pk(p):
    m=re.match(r'pkg:([^/]+)/(.+)@([^@]+)$',p)
    return (m.group(1),urllib.parse.unquote(m.group(2)),m.group(3)) if m else None

print("=== STALENESS vs registry 'latest' (npm, live samples) ===")
import functools
def vkey(v):
    core=re.split(r'[-+]',v)[0]
    return [int(x) if x.isdigit() else 0 for x in core.split('.')[:3]]
buckets=collections.Counter(); affected=collections.Counter()
examples=collections.defaultdict(list)
for x in live:
    ps=x['manifest'].get('packages') or []
    if not ps: continue
    c=pk(ps[0])
    if not c or c[0]!='npm': continue
    d=REG.get(c[1])
    if not d or not d.get('ok') or not d.get('latest'): continue
    cur=vkey(c[2]); lat=vkey(d['latest'])
    if c[2]==d['latest']: b='latest'
    elif cur[0]<lat[0]: b='major behind'
    elif cur[0]==lat[0] and cur[1]<lat[1]: b='minor behind'
    elif cur[0]==lat[0] and cur[1]==lat[1] and cur[2]<lat[2]: b='patch behind'
    else: b='ahead-of-latest (prerelease/tagged)'
    buckets[b]+=1
    if len(examples[b])<6: examples[b].append(f"{c[1]}@{c[2]} (latest {d['latest']})")
tot=sum(buckets.values())
for b,n in buckets.most_common():
    print(f"  {b:34s} {n:5d}  {100*n/tot:5.1f}%")
    for e in examples[b][:4]: print(f"        e.g. {e}")

print()
print("=== SYMBOL/PACKAGE PREFIX COHERENCE (live, samples that declare symbols) ===")
def norm(s): return re.sub(r'[^a-z0-9]','',s.lower())
mismatch=[]; checked=0; nosym=0
for x in live:
    syms=x['manifest'].get('symbols') or []
    ps=x['manifest'].get('packages') or []
    if not syms: nosym+=1; continue
    names=[]
    for p in ps:
        c=pk(p)
        if c: names.append(norm(c[1])); names.append(norm(c[1].split('/')[-1]))
    if not names: continue
    checked+=1
    bad=[]
    for s in syms:
        ns=norm(s)
        head=norm(re.split(r'[.\s/:]',s)[0]) if s else ''
        if not any(n and (ns.startswith(n) or n.startswith(head) and head or head and head in n) for n in names):
            # stricter: does any package name appear in the symbol, or symbol head in package name
            if not any(n and (n in ns or (head and head in n)) for n in names):
                bad.append(s)
    if bad: mismatch.append((x['sampleId'],ps,bad))
print("samples with symbols     :",checked)
print("samples with no symbols  :",nosym)
print("symbol/package unrelated :",len(mismatch), f"({100*len(mismatch)/max(checked,1):.1f}% of symbol-bearing)")
for sid,ps,bad in mismatch[:20]:
    print(f"  {ps}  ->  {bad[:4]}")
json.dump([{"sampleId":s,"packages":p,"symbols":b} for s,p,b in mismatch],open(W+"/symbol_mismatch.json","w"),indent=1)
