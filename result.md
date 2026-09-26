# Issue #513 Result: v0.2.1 released from green main; production carries #149 and #277

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/513
- Branch: `issue/513-p1-release-v0-2-1-from`
- Release tag: `v0.2.1` (annotated, tag object `4e649572451e8e5e7243ac75cf9013da7fe4beec`)
- Target commit: `a6ae2ecb5900f8719e70bb23aabfadd49b4437aa` (main, PR #510 merge)
- Previous production commit: `ebde5fc4d120c23b122a48ae6ea14bbdfde65ac2` (`v0.1.199`)
- `v0.2.0` stays where it was (`4411425`), as the failed tag; no tag was re-pointed.

## Result

`v0.2.1` was cut on the newest main commit with a green CI run including the
`windows` job, released end to end, rolled onto the farm, and deployed to
production. It carries 22 commits over `v0.1.199`, including the #277
`csx doctor` merges (`e9f538f` #507, `6b781c3` #509) and the #149
hard-coordinate cost merges (`f68486d` #508, `a6ae2ec` #510).

| Step | Run | Result |
| --- | --- | --- |
| Main CI on `a6ae2ec` (Windows + Test) | [36018526597](https://github.com/r2cuerdame/CodeSampleX/actions/runs/36018526597) | success, 2026-09-24T15:13Z |
| Release `v0.2.1` | [36208316085](https://github.com/r2cuerdame/CodeSampleX/actions/runs/36208316085) | success, 01:24–01:44Z 2026-09-26 |
| Farm roll `farm -> v0.2.1` | [CodeSampleX-Farm 36209245721](https://github.com/r2cuerdame/CodeSampleX-Farm/actions/runs/36209245721) | success, 01:41–01:43Z |
| Farm health after roll | [CodeSampleX-Farm 36209628751](https://github.com/r2cuerdame/CodeSampleX-Farm/actions/runs/36209628751) | success, 01:48Z |
| Production deploy `a6ae2ec` | [36209557444](https://github.com/r2cuerdame/CodeSampleX/actions/runs/36209557444) | success, 01:46–01:53Z |
| Post-deploy observation | [36209923503](https://github.com/r2cuerdame/CodeSampleX/actions/runs/36209923503) | in progress at hand-off (window up to 135 min) |

Every job in the Release run succeeded: `Validate release tag ref`,
`windows-test`, `build`, `sign`, `defender-scan`,
`Clean Windows signed bootstrap`, `publish`, `Roll the farm`.

## Evidence (re-checkable without this workstation)

1. **Production version** — `curl -s https://codesamplex.dev/version`:

   ```json
   {"service":"csx-server","version":"v0.2.1","revision":"a6ae2ecb5900f8719e70bb23aabfadd49b4437aa","shortRevision":"a6ae2ec","environment":"production","builtAt":"2026-09-26T01:47:54Z"}
   ```

   `https://codesamplex.dev/healthz` → 200.

2. **Stable update manifest** —
   `https://github.com/r2cuerdame/CodeSampleX/releases/latest/download/csx-update-stable.json`
   was verified with the product's own `update.VerifyEnvelope` against the
   release trust root (`CSX_UPDATE_PUBLIC_KEY_B64` in the
   `codesamplex-release-signing` environment, `J2RcMjVOJjOihUnyVsnZOT/fRha+thAqLow2SPQ0ElI=`), channel
   `stable`, on 2026-09-26 about 02:10Z:

   ```
   VERIFIED version=v0.2.1 sequence=36208316085 publishedAt=2026-09-26 01:39:25 +0000 UTC expiresAt=2026-12-25 01:39:25 +0000 UTC assets=6
     darwin/amd64  002ad68f6a649943f132b51220d70333e346f0e522d5592925bd47121a7630bd
     darwin/arm64  17783421ecfeee04f8c736b76a979989bd4a1c87a35e98d224b3f241debbf9af
     linux/amd64   fe659a5963d462cc2aa64b7deb1c6b9b0326ae54f99997118a296f68a3f06f88
     linux/arm64   1d5138462224eeba52eacad555749e180280bb25c05052f3e946cb327d2c8453
     windows/amd64 0abcd82e3fa9d5d3d7a0b98d040498f0bd5b3c4b85e9e56533f224de86c95ce0
     windows/arm64 0131becf50d916c3cffa9afbb6e7df800eebe2090bf3c42f5d168bb4e7a11f9b
   ```

   The manifest sequence equals the Release run id. Independently, a real
   Windows install that was on `v0.1.199` accepted it through its embedded key:
   `csx update status` → `current: v0.2.1`, `highest trusted: v0.2.1
   (sequence 36208316085)`; `csx update check` → `csx v0.2.1 is current on the
   stable channel.`

3. **Farm** — farm-health 36209628751 after the roll:
   `FARM HEALTHY — csx-farm-linux-1: all configured SLOs are within threshold`,
   `versions required=v0.2.1 gen=v0.2.1 verify=v0.2.1`, both slots `active`.

4. **#277 in the shipped binary** — the signed Windows `v0.2.1` payload runs
   `csx doctor` (25 checks: executable, release, payload, launcher,
   update-state/lock, config, auth, caches, local-db, mcp, server, server-api,
   registries, mcp-config). On this workstation it reported one `WARN`
   (`payload-entries`) and one `FAIL` (`auth`: saved API token format) — those
   are this machine's state for #277 QA to judge, not release failures.

## Observations handed on (not in this issue's scope)

- The same farm-health run reports `outputs sample=unknown ago receipt=unknown
  ago; verify/24h00m completed=0`. That 24 h window predates this release; it
  is farm throughput, tracked by r2cuerdame/CodeSampleX-Farm#197.
- `https://codesamplex.dev/v1/stats` still showed `generatedAt`
  `2026-09-17T12:34:46Z` about 25 minutes after the deploy. A restart costs
  a builder pass; whether the stats snapshot moves is for the post-deploy
  observation and #511 to confirm.

## How a verifier re-runs this

```sh
curl -s https://codesamplex.dev/version                      # v0.2.1 / a6ae2ec
gh run view 36208316085 --json jobs --jq '.jobs[]|"\(.name) \(.conclusion)"'
gh run view 36209245721 -R r2cuerdame/CodeSampleX-Farm --json conclusion
git fetch --tags && git rev-parse 'v0.2.1^{}'                # a6ae2ecb59…
csx update check                                             # on any stable install
csx doctor
```

No code changed in this issue: the release is the tag and the pipeline runs
above; this file is the delivery evidence.
