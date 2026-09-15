import json,collections,re,sys
W=sys.argv[1]
S=json.load(open(W+"/samples.json",encoding='utf-8'))
live=[x for x in S if not x['quarantined']]
def m(x): return x['manifest']
def pkgs(x): return tuple(sorted(m(x).get('packages') or []))
def syms(x): return tuple(sorted(m(x).get('symbols') or []))
def goal(x): return ((m(x).get('case') or {}).get('goal') or '').strip()
def contract(x): return tuple(l.strip() for l in ((m(x).get('case') or {}).get('contract') or []))
def base(p):
    mm=re.match(r'pkg:([^/]+)/(.+)@([^@]+)$',p)
    return (mm.group(1),mm.group(2)) if mm else ('?',p)

print("=== EXACT DUPLICATES (same packages+symbols+goal+contract, live) ===")
k=collections.defaultdict(list)
for x in live: k[(pkgs(x),syms(x),goal(x),contract(x))].append(x['sampleId'])
dups={key:v for key,v in k.items() if len(v)>1}
print("dup groups:",len(dups),"  extra samples:",sum(len(v)-1 for v in dups.values()))
for key,v in sorted(dups.items(),key=lambda kv:-len(kv[1]))[:8]:
    print(f"  x{len(v)} {key[0]} sym={key[1]} goal={key[2][:60]!r}")

print()
print("=== SEMANTIC DUPLICATES (same package-version + same symbol set + same contract text, differing goal) ===")
k2=collections.defaultdict(list)
for x in live: k2[(pkgs(x),syms(x),contract(x))].append(x['sampleId'])
d2={key:v for key,v in k2.items() if len(v)>1}
print("groups:",len(d2),"  extra:",sum(len(v)-1 for v in d2.values()))

print()
print("=== SAME PACKAGE+VERSION+SYMBOLS (any contract) ===")
k3=collections.defaultdict(list)
for x in live: k3[(pkgs(x),syms(x))].append(x['sampleId'])
d3={key:v for key,v in k3.items() if len(v)>1}
print("groups:",len(d3),"  extra:",sum(len(v)-1 for v in d3.values()))
for key,v in sorted(d3.items(),key=lambda kv:-len(kv[1]))[:10]:
    print(f"  x{len(v)} {key[0]} sym={key[1]}")

print()
print("=== SATURATION: samples per package(name, all versions), live ===")
pc=collections.Counter()
for x in live:
    ps=m(x).get('packages') or []
    if ps: pc[base(ps[0])]+=1
print("distinct package names:",len(pc))
print("top 25 by sample count:")
for (e,n),c in pc.most_common(25): print(f"  {c:5d}  {e}/{n}")
tot=sum(pc.values())
top50=sum(c for _,c in pc.most_common(50))
print(f"\ntop-50 packages hold {top50}/{tot} = {100*top50/tot:.1f}% of live samples")
sing=sum(1 for c in pc.values() if c==1)
print(f"packages with exactly 1 sample: {sing} ({100*sing/len(pc):.1f}%)")

print()
print("=== SATURATION: samples per package@version ===")
pv=collections.Counter()
for x in live:
    ps=m(x).get('packages') or []
    if ps: pv[ps[0]]+=1
print("distinct package@version:",len(pv))
for p,c in pv.most_common(15): print(f"  {c:5d}  {p}")
