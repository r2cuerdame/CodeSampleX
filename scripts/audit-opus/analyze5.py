import json,sys,collections,re,urllib.parse
W=sys.argv[1]
S=json.load(open(W+"/samples.json",encoding='utf-8'))
R=json.load(open(W+"/receipts.json",encoding='utf-8'))
SP=[l.rstrip('\n').split('\t') for l in open(W+"/sample_packages.tsv",encoding='utf-8') if l.strip()]
live=[x for x in S if not x['quarantined']]

print("=== PURL CANONICALIZATION DEFECTS (manifest packages, ALL samples) ===")
issues=collections.Counter(); ex=collections.defaultdict(list)
allp=collections.Counter()
for x in S:
    for p in (x['manifest'].get('packages') or []):
        allp[p]+=1
        m=re.match(r'pkg:([^/]+)/(.+)@([^@]+)$',p)
        if not m: issues['unparseable']+=1; ex['unparseable'].append(p); continue
        eco,name,ver=m.groups()
        if eco=='golang' and not ver.startswith('v'):
            issues['golang version missing v-prefix']+=1; ex['golang version missing v-prefix'].append(p)
        if eco=='npm' and name.startswith('@'):
            issues['npm scope unencoded (@ not %40)']+=1; ex['npm scope unencoded (@ not %40)'].append(p)
        if name!=name.lower() and eco in ('npm','pypi'):
            issues['non-lowercased name']+=1; ex['non-lowercased name'].append(p)
        if eco=='pypi' and '_' in name:
            issues['pypi underscore (not normalized to -)']+=1; ex['pypi underscore (not normalized to -)'].append(p)
print("distinct purls in manifests:",len(allp))
for k,v in issues.most_common():
    print(f"  {k:36s} {v:5d}   e.g. {sorted(set(ex[k]))[:3]}")

print()
print("=== SAME RELEASE UNDER TWO SPELLINGS ===")
def canon(p):
    m=re.match(r'pkg:([^/]+)/(.+)@([^@]+)$',p)
    if not m: return p
    eco,name,ver=m.groups()
    name=urllib.parse.unquote(name).lower()
    if eco=='golang': ver=ver if ver.startswith('v') else 'v'+ver
    if eco=='pypi': name=name.replace('_','-')
    return f"pkg:{eco}/{name}@{ver}"
groups=collections.defaultdict(set)
for p in allp: groups[canon(p)].add(p)
split={c:v for c,v in groups.items() if len(v)>1}
print("releases represented by >1 purl spelling:",len(split))
for c,v in sorted(split.items())[:15]:
    print(f"  {c}  <-  {sorted(v)}")
aff=sum(allp[p] for v in split.values() for p in v)
print("manifest package rows affected:",aff)

print()
print("=== sample_packages TABLE (what the site indexes) vs manifest ===")
spby=collections.defaultdict(set)
for sid,purl in SP: spby[sid].add(purl)
diff=0; exd=[]
for x in S:
    dm=set(x['manifest'].get('packages') or [])
    ds=spby.get(x['sampleId'],set())
    if dm and ds and dm!=ds:
        diff+=1
        if len(exd)<6: exd.append((x['sampleId'][:20],sorted(dm),sorted(ds)))
print("sample_packages rows:",len(SP)," samples indexed:",len(spby))
print("samples whose indexed purls != manifest purls:",diff)
for e in exd: print("  ",e)
missing_idx=[x['sampleId'] for x in live if x['sampleId'] not in spby]
print("LIVE samples with NO row in sample_packages:",len(missing_idx))
multi=[s for s,v in spby.items() if len(v)>1]
print("samples indexed under >1 purl:",len(multi))
