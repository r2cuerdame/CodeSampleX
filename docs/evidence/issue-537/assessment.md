# Issue #537 production window

Local execution of the same reviewed SSH collector, using the existing pinned
production key and revision `a9fe24839bae5dfda10da04dcd7d6cb99e3674c7`.
The exact 20-round artifact is [`p95-window.json`](p95-window.json).
Window: 2026-10-05 18:54:19.462–18:56:07.530 UTC. All three paths returned
20/20 HTTP 200 responses.

| Path | Local Caddy TTFB p95 | At that p95: host CPU steal | Forwarded upstream app phases at that p95 | PostgreSQL snapshot after that round |
| --- | ---: | ---: | --- | --- |
| `/v1/stats` | 235.14 ms | 80.50% | middleware 125.115 ms, db_wait 0, handler 0.002 ms, serialization 0.902 ms | active 0, no wait, ungranted locks 0 |
| `/healthz` | 323.48 ms | 76.36% | middleware 0.010 ms, db_wait 0.002 ms, handler 90.253 ms, serialization 0.003 ms | active 0, no wait, ungranted locks 0 |
| `/v1/shards/npm/zod/3` | 312.77 ms | 79.69% | middleware 0.060 ms, db_wait 0.004 ms, handler 234.846 ms, serialization 0.161 ms | active 0, no wait, ungranted locks 0 |

Host CPU steal over the full 108-second window was **77.64%**. The 20
PostgreSQL snapshots all succeeded: maximum active backends 4, maximum
ungranted locks 0, eight `IO` wait observations and three `running`
observations. `pg_stat_statements` top-five historical max-time entries did
not change call counts across the window. That historical top-five view cannot
exclude a slower statement outside its list or an unobserved short wait.
The three local TTFB p95 values did not reproduce the public SLO alert in this
window; the SLO uses runner-side server time, so these local values are also a
different vantage and metric.

The common high steal and near-zero route `db_wait` narrow the first candidate
to host CPU scheduling contention. A PostgreSQL lock bottleneck is unsupported
by these samples. Caddy forwarded the application's `Server-Timing` phases;
their sum is the observed upstream application portion. The residual between
local Caddy TTFB and that sum was also material (route p95 156.54, 233.21,
157.01 ms respectively), but includes TLS, proxy scheduling and transfer.
The existing privacy-safe Caddy log has no duration or upstream dial field,
so this run cannot isolate Caddy dial wait from that residual. These data do
not establish an application code defect. #531 should treat host contention
as the leading hypothesis and retain the Caddy residual as an uncertainty.
