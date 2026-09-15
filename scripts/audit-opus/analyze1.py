import json,collections,re,hashlib,sys
W=sys.argv[1]
S=json.load(open(W+"/samples.json",encoding='utf-8'))
R=json.load(open(W+"/receipts.json",encoding='utf-8'))
live=[x for x in S if not x['quarantined']]
print("=== CORPUS ===")
print("total samples      :",len(S))
print("quarantined        :",len(S)-len(live), f"({100*(len(S)-len(live))/len(S):.1f}%)")
print("live samples       :",len(live))

# receipts index
byS=collections.defaultdict(list)
for r in R: byS[r['sampleId']].append(r)
passing={sid for sid,rs in byS.items() if any(r['contractResult']=='PASS' for r in rs)}
print("samples w/ >=1 receipt:",len(byS))
print("samples w/ PASS      :",len(passing))
live_ids={x['sampleId'] for x in live}
print("LIVE w/ PASS receipt :",len(live_ids & passing))
print("LIVE w/o any receipt :",len(live_ids - set(byS)))
print("LIVE w/ receipts but no PASS:",len(live_ids & set(byS) - passing))
print("receipts w/ NO sample row   :",len(set(byS)-{x['sampleId'] for x in S}))

print()
print("=== GOAL QUALITY (live) ===")
placeholder=re.compile(r'^verify\s+pkg:',re.I)
gl=collections.Counter()
ph=[]
empt=[]
for x in live:
    g=(x['manifest'].get('case') or {}).get('goal','') or ''
    g=g.strip()
    if not g: empt.append(x['sampleId']); gl['empty']+=1
    elif placeholder.match(g): ph.append(x); gl['placeholder']+=1
    else: gl['authored']+=1
for k,v in gl.items(): print(f"  {k:12s} {v:6d}  {100*v/len(live):5.1f}%")

print()
print("=== CONTRACT QUALITY (live) ===")
cc=collections.Counter()
nocontract=[]
for x in live:
    c=(x['manifest'].get('case') or {}).get('contract') or []
    c=[l for l in c if isinstance(l,str) and l.strip()]
    if len(c)==0: cc['0-empty']+=1; nocontract.append(x['sampleId'])
    elif len(c)==1: cc['1-line']+=1
    elif len(c)<=3: cc['2-3 lines']+=1
    else: cc['4+ lines']+=1
for k,v in sorted(cc.items()): print(f"  {k:12s} {v:6d}  {100*v/len(live):5.1f}%")

print()
print("=== CONTRACT COMMAND / RUNNABILITY (live) ===")
nocmd=[x['sampleId'] for x in live if not x['manifest'].get('contractCommand')]
noadapter=[x['sampleId'] for x in live if not x['manifest'].get('verifierAdapter')]
nopkg=[x['sampleId'] for x in live if not x['manifest'].get('packages')]
nosym=[x['sampleId'] for x in live if not x['manifest'].get('symbols')]
print("  no contractCommand :",len(nocmd))
print("  no verifierAdapter :",len(noadapter))
print("  no packages        :",len(nopkg))
print("  no symbols         :",len(nosym))

print()
print("=== ECOSYSTEM SPREAD (live) ===")
def eco(p):
    m=re.match(r'pkg:([^/]+)/',p or '')
    return m.group(1) if m else '?'
ecos=collections.Counter()
for x in live:
    ps=x['manifest'].get('packages') or []
    ecos[eco(ps[0]) if ps else '?']+=1
for k,v in ecos.most_common(): print(f"  {k:12s} {v:6d}  {100*v/len(live):5.1f}%")
