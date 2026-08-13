# Public v1 E2E Report (goal.md §25)

Run: 2026-08-13 10:12 UTC — server: docker compose e2e stack, client: csx.exe (windows/amd64), peers: 2

| Scenario | Result |
|----------|--------|
| A - automatic evidence | PASS |
| B - failure evidence | PASS |
| D - sample contribution + cross verify | PASS |
| C - search + reuse | PASS |
| E - private protection | PASS |
| F - outage resilience | PASS |

## Evidence log

- server healthy at http://localhost:8089
- two community peers initialized (home1:48619, home2:48719)
- A: csx run exit=0; csx sync exit=0
- A: axios@1.12.0 visible in registry with evidence
- B: failure cluster recorded with error code, no path/project leak in server response
- D: created sha256:b8608326e1fddf33b6483b42b583c96aae0108447565c87516a0464315ee9064
- D: origin verify exit=0
- D: published anonymously
- D: final sample status = CROSS_PASS
- C: search HIT grade=REFERENCE_ONLY sample=sha256:b8608326e1fddf33b6483b42b583c96aae0108447565c87516a0464315ee9064
- C: adoption reported; local stats + privacy preview served by daemon
- E: private file: dependency fully absent from server (404 + no stats trace)
- F: server stopped
- F: csx run and local search work with server down
- F: queued evidence uploaded after recovery
