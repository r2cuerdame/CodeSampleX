U="https://codesamplex.dev/golang/github.com/google/uuid"
echo "phase 1: hammer until a 200"
for i in $(seq 1 25); do
  r=$(curl -s -o /dev/null -w "%{http_code}:%{time_starttransfer}" --max-time 45 "$U")
  echo "  try$i $r"
  if [ "${r%%:*}" = "200" ]; then
     echo "phase 2: immediate follow-ups after the 200 (cache should be warm for 30m)"
     for j in 1 2 3 4 5; do
       echo "  after$j $(curl -s -o /dev/null -w '%{http_code}:%{time_starttransfer}' --max-time 45 "$U")"
       sleep 1
     done
     exit 0
  fi
  sleep 1
done
echo "never succeeded in 25 tries"
