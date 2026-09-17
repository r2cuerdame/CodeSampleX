# REST quick start — read CodeSampleX with nothing installed

CodeSampleX answers one question — *does it run there?* — from builds that
really ran. Reading the network needs no key, no account and no client
library: every endpoint below is a plain HTTPS request that answers JSON,
from any origin. The REST surface is the body of the product; the CLI, the
MCP server and any future agent protocol are adapters over the same data.

**REST reads the network. The CLI lets the network observe reality.**
Install the CLI only when you want CodeSampleX to observe and verify what
actually happens on a machine: real builds, sanitized structured evidence,
the automatic failed-build hook, the privacy preview, publishing, the worker.
The full capability comparison is in the [README](../README.md#no-install-needed-to-read-the-network)
and on [/features](https://codesamplex.dev/features).

For an agent that can fetch a URL and nothing else, the same contract as a
machine-readable guide: **<https://codesamplex.dev/skill.md>**.

## 1. Search, graded against your environment

```bash
curl 'https://codesamplex.dev/v2/search?package=pkg:npm/axios&symbol=axios.post&os=linux&runtime=node&runtimeVersion=22'
```

Parameters: `q`, `package` (repeatable, a PURL), `symbol` (repeatable),
`errorCode`, `ecosystem`, `os`, `arch`, `runtime`, `runtimeVersion`,
`packageManager`, `moduleSystem`, `executionContext`, `libc`, `limit`. Give
the dimensions you actually know and none you are guessing at; an empty
request is refused with a sentence that names the parameters.

The same question as a JSON body, for callers that prefer it:

```bash
curl -s https://codesamplex.dev/v2/search \
  -H 'Content-Type: application/json' \
  -d '{"schemaVersion":2,"query":"post JSON with axios","packages":["pkg:npm/axios@1.12.0"],"symbols":["axios.post"],"environment":{"schemaVersion":1,"os":"linux","runtime":"node","runtimeVersion":"22.18.1"}}'
```

The answer, whichever door it came through:

| field | meaning |
|---|---|
| `grade` | the best match: `EXACT`, `COMPATIBLE`, `ADAPTATION_REQUIRED`, `REFERENCE_ONLY` or `NO_SAFE_MATCH` |
| `miss` | `true` when nothing has been proven for this case |
| `results[].match` | that result's own grade |
| `results[].sampleId`, `results[].sampleUrl` | the verified sample and its page |
| `results[].exact[]`, `results[].different[]` | which environment dimensions matched the one the sample ran in, and which did not |
| `results[].adaptationNeeded[]` | concrete changes you must make before the sample applies |
| `results[].evidence` | observation counts kept apart from contract passes — a project compiling is never a symbol working |
| `results[].knownFailures[]` | elevated failures recorded for the coordinate |
| `observed` | on a miss: peer-reported outcomes, labelled `basis: "observed"` — observation, never verification |

`NO_SAFE_MATCH` is a real answer. It means the network holds nothing it built
and ran for your case: solve it fresh, and do not present a pattern you
recall as something that ran. A nearby environment is never promoted to an
exact one — read `different[]` before `match`.

## 2. One package, one symbol

```bash
# percent-encode the slash in the PURL
curl 'https://codesamplex.dev/v1/registry/packages/pkg:npm%2Faxios@1.12.0'
curl 'https://codesamplex.dev/v1/registry/symbols/npm/axios/axios.post'
```

## 3. A sample's manifest, receipts and files

```bash
curl 'https://codesamplex.dev/v1/samples/<sampleId>'
curl -o sample.tar.gz 'https://codesamplex.dev/v1/samples/<sampleId>/artifact'
```

The id is the SHA-256 of the contents, so the bytes verify themselves.

## 4. Findings, gaps, adapters, stats

```bash
curl 'https://codesamplex.dev/findings.json?eco=npm'
curl 'https://codesamplex.dev/v1/wanted'
curl 'https://codesamplex.dev/v1/adapters'
curl 'https://codesamplex.dev/v1/stats'
```

## 5. Optional: an execution footprint

If your task actually ran a sample the network returned — built it,
type-checked it, tested it, executed it — you may report the outcome. Do no
extra work to produce one.

```bash
curl -s https://codesamplex.dev/v1/footprints/execution \
  -H 'Content-Type: application/json' \
  -d '{"schemaVersion":1,"sampleId":"<sampleId>","outcome":"pass","stage":"build","environment":{"os":"linux","arch":"amd64","runtime":"node","runtimeVersion":"22.18.1"}}'
```

Every field is a short token from a closed set (`outcome`: `pass`, `fail`,
`could_not_run`; `stage`: `build`, `typecheck`, `test`, `runtime`; an
optional `failureFingerprint` is a 64-hex normalized value). The endpoint
refuses free text, error messages, paths and logs. The row is filed under
`EXECUTION_FOOTPRINT`, an **unsigned self-report** kept apart from the
sanitized, client-correlated evidence the CLI captures: it weighs nothing in
any grade or confidence and never promotes a sample past what a signed
receipt proved. The response says so in its `note`.

## Limits and boundaries

- Reads are rate-limited per client; a `429` means slow down, not retry in a
  loop.
- No route on this page asks for or stores a token, a cookie, a path, a
  repository name or a log line.
- `GET /version` says which build of the server answered.
