#!/usr/bin/env bash
# Safe read-only TTFB/total timing probe of public pages (GET only).
urls=(
 "https://codesamplex.dev/healthz"
 "https://codesamplex.dev/"
 "https://codesamplex.dev/compatibility"
 "https://codesamplex.dev/dependencies"
 "https://codesamplex.dev/gaps"
 "https://codesamplex.dev/findings"
 "https://codesamplex.dev/samples"
 "https://codesamplex.dev/stats"
 "https://codesamplex.dev/features"
 "https://codesamplex.dev/adapters"
 "https://codesamplex.dev/golang/github.com%2Fjackc%2Fpgx%2Fv5"
 "https://codesamplex.dev/npm/hono"
 "https://codesamplex.dev/npm/@babel/core"
 "https://codesamplex.dev/golang/golang.org%2Fx%2Fsys"
 "https://codesamplex.dev/pypi/pymatting/1.1.15/samples/knn-laplacian-64824931"
 "https://codesamplex.dev/v1/stats"
 "https://codesamplex.dev/v1/wanted"
 "https://codesamplex.dev/v1/shards/npm/hono/4"
 "https://codesamplex.dev/sitemap.xml"
 "https://codesamplex.dev/sitemaps/packages-1.xml"
 "https://codesamplex.dev/sitemaps/samples-1.xml"
)
printf "%-8s %-8s %-8s %-9s %-5s %s\n" round ttfb_s total_s bytes code url
for round in 1 2; do
 for u in "${urls[@]}"; do
  out=$(curl -s -o /dev/null -w "%{time_starttransfer} %{time_total} %{size_download} %{http_code}" --max-time 70 "$u")
  printf "%-8s %-8s %-8s %-9s %-5s %s\n" "$round" $out "$u"
 done
done
