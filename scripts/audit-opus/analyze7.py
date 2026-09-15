import json,sys,collections
W=sys.argv[1]
S=json.load(open(W+"/samples.json",encoding='utf-8'))
live=[x for x in S if not x['quarantined']]
def key(x):
    m=x['manifest']
    return (tuple(sorted(m.get('packages') or [])), tuple(sorted(m.get('symbols') or [])))
g=collections.defaultdict(list)
for x in live: g[key(x)].append(x)
dups={k:v for k,v in g.items() if len(v)>1}
print("live duplicate groups (same packages+symbols):",len(dups))
after=0; total_extra=0; dates=[]
for k,v in dups.items():
    v=sorted(v,key=lambda x:x['createdAt'])
    total_extra+=len(v)-1
    for x in v[1:]:
        dates.append(x['createdAt'][:10])
        if x['createdAt']>'2026-08-19': after+=1
print("redundant live samples:",total_extra)
print("created AFTER the 2026-08-19 dedup pass:",after, f"({100*after/total_extra:.0f}%)")
print("by month:",dict(sorted(collections.Counter(d[:7] for d in dates).items())))
print()
print("=== placeholder-goal samples by month (live) ===")
import re
ph=[x for x in live if re.match(r'^verify\s+pkg:',((x['manifest'].get('case') or {}).get('goal') or ''),re.I)]
print("total:",len(ph))
print(dict(sorted(collections.Counter(x['createdAt'][:7] for x in ph).items())))
print("most recent placeholder sample:",max(x['createdAt'] for x in ph))
print()
print("=== live samples with NO symbols, by month ===")
ns=[x for x in live if not (x['manifest'].get('symbols') or [])]
print("total:",len(ns), dict(sorted(collections.Counter(x['createdAt'][:7] for x in ns).items())))
print("most recent:",max(x['createdAt'] for x in ns))
print()
print("=== corpus growth by month (live) ===")
print(dict(sorted(collections.Counter(x['createdAt'][:7] for x in live).items())))
