#!/bin/sh
# Read-only, bounded production host sample for issue #531.
set -eu
printf 'utc='; date -u +%Y-%m-%dT%H:%M:%SZ
printf 'load='; cat /proc/loadavg
before=$(awk '/^cpu / {print $2,$3,$4,$5,$6,$7,$8,$9}' /proc/stat)
sleep 5
after=$(awk '/^cpu / {print $2,$3,$4,$5,$6,$7,$8,$9}' /proc/stat)
awk -v b="$before" -v a="$after" 'BEGIN {split(b,x," "); split(a,y," "); total=0; for(i=1;i<=8;i++) total+=y[i]-x[i]; printf "steal_pct=%.2f total_ticks=%.0f steal_ticks=%.0f\n",100*(y[8]-x[8])/total,total,y[8]-x[8]}'
printf 'memory_host='; awk '/^MemTotal:|^MemAvailable:/ {printf "%s=%sKiB ",$1,$2} END {print ""}' /proc/meminfo
docker inspect --format 'server_pid={{.State.Pid}} restart_count={{.RestartCount}} oom_killed={{.State.OOMKilled}} started_at={{.State.StartedAt}} memory_limit={{.HostConfig.Memory}}' codesamplex-server-1
docker stats --no-stream --format 'container={{.Name}} cpu={{.CPUPerc}} memory={{.MemUsage}} memory_pct={{.MemPerc}}' codesamplex-server-1 codesamplex-caddy-1 codesamplex-db-1
docker exec codesamplex-server-1 sh -c 'printf "server_processes\n"; ps -o pid,comm,rss,vsz; printf "server_pid1_status\n"; grep -E "^(Name|VmRSS|RssAnon|RssFile|RssShmem|VmSwap|Threads):" /proc/1/status; printf "server_memory_current="; cat /sys/fs/cgroup/memory.current; printf "server_memory_max="; cat /sys/fs/cgroup/memory.max; printf "server_memory_stat\n"; grep -E "^(anon|file|kernel|slab|active_file|inactive_file|workingset_refault_file|pgmajfault) " /sys/fs/cgroup/memory.stat; printf "server_memory_events\n"; cat /sys/fs/cgroup/memory.events; printf "go_memory_limit="; tr "\000" "\n" </proc/1/environ | grep "^GOMEMLIMIT="'
printf 'ops_runtime_http='; docker exec codesamplex-server-1 wget -q -S -O /dev/null -T 3 http://127.0.0.1:8080/v1/ops/pool-metrics 2>&1 | grep -m 1 'HTTP/' || true
# End marker also makes CRLF appended by a PowerShell stdin pipe harmless.
