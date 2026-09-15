import base64, json, sys
def load(path):
    out=[]
    with open(path,'r',encoding='utf-8') as f:
        for line in f:
            line=line.strip()
            if not line: continue
            out.append(json.loads(base64.b64decode(line).decode('utf-8')))
    return out
s=load(sys.argv[1])
json.dump(s, open(sys.argv[2],'w',encoding='utf-8'))
print(len(s))
