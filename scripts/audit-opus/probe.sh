#!/bin/bash
N=${2:-10}
U="$1"
codes=""; times=""
for i in $(seq 1 $N); do
  r=$(curl -s -o /dev/null -w "%{http_code}:%{time_total}" --max-time 40 "$U")
  codes="$codes ${r%%:*}"; times="$times ${r##*:}"
  sleep 0.4
done
ok=$(echo $codes | tr ' ' '\n' | grep -c '^200$')
echo "$ok/$N ok | codes:$codes"
echo "   times:$times" | python -c "
import sys
line=sys.stdin.read().split(':',1)[1].split()
v=sorted(float(x) for x in line)
import statistics
print('   n=%d  min=%.2f  med=%.2f  p90=%.2f  max=%.2f'%(len(v),v[0],statistics.median(v),v[int(len(v)*0.9)-1],v[-1]))
"
