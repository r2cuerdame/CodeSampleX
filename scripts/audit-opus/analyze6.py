import json,sys,collections,re,urllib.parse
W=sys.argv[1]
S=json.load(open(W+"/samples.json",encoding='utf-8'))
SP=[l.split() for l in open(W+"/sample_packages.txt",encoding='utf-8') if l.strip()]
live=[x for x in S if not x['quarantined']]
GOSTD={'embed','encoding/json','net/http','fmt','os','io','strings','bytes','time','errors','context',
 'sync','testing','crypto','database/sql','log','math','path','regexp','sort','strconv','text/template',
 'html/template','net/url','encoding/base64','encoding/csv','encoding/xml','bufio','flag','reflect','unicode','runtime','slices','maps','cmp','iter','structs','weak','unique','log/slog','crypto/rand','net'}
print("=== GO STDLIB RECORDED AS A MODULE (all samples) ===")
hits=collections.Counter(); samples=collections.Counter()
for x in S:
    for p in (x['manifest'].get('packages') or []):
        m=re.match(r'pkg:golang/(.+)@([^@]+)$',p)
        if not m: continue
        name,ver=m.groups()
        if '.' not in name.split('/')[0]:   # no dot in first path element => not a module domain
            hits[p]+=1; samples[x['sampleId']]+=1
print("distinct stdlib-shaped purls:",len(hits)," package rows:",sum(hits.values())," samples:",len(samples))
for p,n in hits.most_common(30): print(f"  {n:4d}  {p}")

print()
print("=== sample_packages INDEX vs MANIFEST ===")
spby=collections.defaultdict(set)
for sid,purl in SP: spby[sid].add(purl)
print("samples indexed:",len(spby),"rows:",len(SP))
liveids={x['sampleId'] for x in live}
noidx=[s for s in liveids if s not in spby]
print("LIVE samples with NO sample_packages row:",len(noidx))
diff=[];
for x in S:
    dm=set(x['manifest'].get('packages') or []); ds=spby.get(x['sampleId'],set())
    if dm and ds and dm!=ds: diff.append((x['sampleId'],sorted(dm),sorted(ds)))
print("indexed purls != manifest purls:",len(diff))
for e in diff[:8]: print("  ",e[0][:20],e[1],"->",e[2])
multi=[s for s,v in spby.items() if len(v)>1]
print("samples indexed under >1 purl:",len(multi))
# multi-package manifests
mp=[x for x in live if len(x['manifest'].get('packages') or [])>1]
print("live samples declaring >1 package:",len(mp))
