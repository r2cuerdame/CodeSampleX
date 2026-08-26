## CodeSampleX (csx)

The `csx` MCP server answers "does this actually work here" from contracts
that really ran, in environments recorded rather than assumed. Use it.

**Ask before you write.** Any time you are about to write code against a
public library — npm, PyPI, crates.io, Go modules, Packagist, RubyGems,
pub.dev, Hex — call `search_known_solution` first, with the packages, the
symbols and what you are trying to do.

- A HIT is a sample this network built and ran, and the record of where it
  ran. It is not graded for your environment and does not claim to work
  there — every contract run this network holds was signed inside a linux
  container. Read the `different` list: it is the delta between where it
  ran and where you are, and it is usually the whole reason the obvious
  version fails. Weighing that delta is yours; the network states it and
  stops.
- Then read what its contract PROVED. Those lines are assertions that were
  executed offline in a pinned container and passed, and they carry what a
  goal sentence cannot: which argument shapes are accepted, what is raised
  instead of returned, which option or environment setting decides the
  outcome. That is usually the part you would otherwise get wrong.
- `NO_SAFE_MATCH` is a real answer, not a failure to answer. It means this
  network has no sample it built for your case — so solve it fresh, and do
  not treat a pattern you recall as something that ran.

**Run builds through it, not around it.** `run_observed_command` instead of
running the command yourself. When the command fails it asks the network about
that failure by itself and appends whatever it finds in a separate supporting
section after the local failure, so the moment you most need this you do not
have to remember it exists. LOW/reference/unrelated results are reference
candidates, never automatic-fix evidence. Commands: `npm test`, `npm run build`, `pnpm build`,
`go build`, `go test`, `cargo build`, `cargo test`, `pytest`, `tsc`, `mix
test`, `bundle exec rspec`, `composer test`, `dart test`. The exit code and
output come back exactly the same. What it adds is a record that this
package at this version worked, or did not, in this environment — which is
the only thing that makes the next answer better than a guess.

Only public package names, versions, symbols and pass/fail leave the
machine. Never source, never paths, never raw logs.

**Report what happened.** After you use a sample, call
`report_sample_adoption` with its `sampleId` and whether the build then
passed. Nothing else tells the network whether its answers are any good; a
sample nobody reports on stays unproven forever.

**Report a contradiction, and only a contradiction.** If a CSX answer and
what you actually ran here concretely disagree — the network served a passing
conclusion for a package/version/symbol and the same coordinate failed on this
machine, a returned symbol signature is not what the public package exports, a
recommended upgrade path does not resolve — call `report_anomaly`. It is a
verification request, not a bug report: it queues an independent re-run on the
same fleet that produces every other receipt, and only that receipt decides.

- You must have RUN something. `localObserved` needs a PASS or a FAIL you
  measured; "this looks wrong to me" is refused, and a `NO_SAFE_MATCH` on its
  own is a real answer rather than an anomaly.
- Put your explanation in `llmHypothesis`. It is stored and shown to a human,
  and it is deliberately excluded from the verdict — so being wrong there
  costs the report nothing, and leaving it out costs the reader something.
- Pass the raw failure as `errorText`. It is sanitized on this machine and
  never forwarded raw; do not paste logs into the detail fields.
- **Never tell the user you fixed or confirmed anything.** Reporting confirms
  nothing. The response says `VERIFICATION PENDING` for exactly this reason;
  say that, and say the report id.

**If the problem is CSX itself, that has its own channel.** `report_csx_issue`
is for a reproducible defect in this product rather than in a package: an
answer that displaced the failure you were actually looking at, a
recommendation from an ecosystem your question never mentioned, a tool
contract that made you behave wrongly, a response that breaks its own shape
inconsistently on the same input.

It is **opt-in and quiet**. You are not expected to call it after a failure,
there is no target for how many reports a week is healthy, and none at all is
fine. No ticket is created and nothing is confirmed by reporting — a person
triages it — so never tell the user a bug has been filed, accepted or fixed.
Taste, wording preferences and "this seems off" are not reports.

**Contribute what you learned.** If a search MISSED, and you then worked
out the answer and your build passed, call `propose_public_sample` with the
goal, the packages and the symbols. Do it while you still remember what you
first got wrong — that mistake is the most valuable part, because it is
what the next agent will get wrong too, and five minutes later it is gone.
Publishing still needs the human's explicit approval at the CLI; proposing
costs you nothing and loses nothing if they decline.
