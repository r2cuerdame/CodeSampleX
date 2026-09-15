import json,sys,collections,re,urllib.parse
W=sys.argv[1]
S=json.load(open(W+"/samples.json",encoding='utf-8'))
R=json.load(open(W+"/receipts.json",encoding='utf-8'))
live=[x for x in S if not x['quarantined']]

BUILTIN_HEADS={'require','import','globalThis','process','console','JSON','Math','Promise','Object',
 'Array','String','Number','Boolean','Date','RegExp','Map','Set','WeakMap','WeakSet','Symbol','Proxy',
 'Reflect','Error','TypeError','Buffer','URL','URLSearchParams','AbortController','AbortSignal',
 'setTimeout','setInterval','queueMicrotask','structuredClone','fetch','Response','Request','Headers',
 'FormData','Blob','File','TextEncoder','TextDecoder','crypto','performance','BigInt','Intl',
 'ArrayBuffer','Uint8Array','DataView','Atomics','WebAssembly','eval','parseInt','parseFloat'}
NODE_CORE={'fs','path','os','util','events','stream','http','https','net','crypto','child_process',
 'worker_threads','assert','buffer','url','zlib','tls','dns','readline','querystring','timers','vm','module','perf_hooks'}

print("=== BUILTIN / NON-PACKAGE SYMBOLS DECLARED AS PACKAGE SYMBOLS (live) ===")
hits=[]
for x in live:
    syms=x['manifest'].get('symbols') or []
    ps=x['manifest'].get('packages') or []
    pnames={urllib.parse.unquote(re.match(r'pkg:[^/]+/(.+)@',p).group(1)) for p in ps if re.match(r'pkg:[^/]+/(.+)@',p)}
    shorts={n.split('/')[-1] for n in pnames}|pnames
    bad=[]
    for s in syms:
        head=re.split(r'[.\(\[:]',s)[0].strip()
        if head in BUILTIN_HEADS or (head in NODE_CORE and head not in shorts):
            bad.append(s)
        elif s.strip() in ('require','import()','import.meta.url'):
            bad.append(s)
    if bad: hits.append({"sampleId":x['sampleId'],"packages":ps,"badSymbols":bad})
print("samples affected:",len(hits), f"({100*len(hits)/len(live):.2f}% of live)")
c=collections.Counter(b for h in hits for b in h['badSymbols'])
for s,n in c.most_common(25): print(f"  {n:4d}  {s}")
json.dump(hits,open(W+"/builtin_symbols.json","w"),indent=1)

print()
print("=== SYMBOL SPELLING CONVENTION SPLIT (live, symbol-bearing) ===")
qual=bare=0
for x in live:
    for s in (x['manifest'].get('symbols') or []):
        ps=x['manifest'].get('packages') or []
        pn={urllib.parse.unquote(re.match(r'pkg:[^/]+/(.+)@',p).group(1)) for p in ps if re.match(r'pkg:[^/]+/(.+)@',p)}
        if any(s.startswith(n+'.') or s.startswith(n.split('/')[-1]+'.') or s==n for n in pn): qual+=1
        else: bare+=1
print(f"  package-qualified spelling: {qual}")
print(f"  bare / other spelling     : {bare}")
print(f"  -> {100*bare/(qual+bare):.1f}% of symbol records are NOT package-qualified")

print()
print("=== RECEIPT v2 resolvedPackages coverage ===")
tot=len(R); withrp=0; mismatched=0; ex=[]
bySample={x['sampleId']:x for x in S}
for r in R:
    rp=r['receipt'].get('resolvedPackages')
    if rp:
        withrp+=1
        s=bySample.get(r['sampleId'])
        if s:
            declared=set(s['manifest'].get('packages') or [])
            got=set(rp if isinstance(rp,list) else [])
            if declared and got and declared!=got:
                mismatched+=1
                if len(ex)<5: ex.append((r['sampleId'][:22],sorted(declared)[:2],sorted(got)[:2]))
print(f"  receipts total              : {tot}")
print(f"  receipts with resolvedPackages: {withrp} ({100*withrp/tot:.1f}%)")
print(f"  declared != resolved         : {mismatched}")
for e in ex: print("   ",e)

print()
print("=== SCHEMA VERSION SPREAD ===")
print(" receipt schemaVersion:",collections.Counter(r['receipt'].get('schemaVersion') for r in R))
print(" sandboxCapability   :",collections.Counter(r['receipt'].get('sandboxCapability') for r in R))
print(" stages.contract     :",collections.Counter(str((r['receipt'].get('stages') or {}).get('contract')) for r in R))
print(" stages.compile      :",collections.Counter(str((r['receipt'].get('stages') or {}).get('compile')) for r in R))
