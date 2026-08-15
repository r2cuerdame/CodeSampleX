## CodeSampleX (csx)

The `csx` MCP server answers "does this actually work here" from contracts
that really ran, in environments recorded rather than assumed. Use it.

**Ask before you write.** Any time you are about to write code against a
public library — npm, PyPI, crates.io, Go modules, Packagist, RubyGems,
pub.dev, Hex — call `search_known_solution` first, with the packages, the
symbols and what you are trying to do.

- A HIT is a verified answer already graded against your environment. Read
  the `different` list: it is the delta between where it ran and where you
  are, and it is usually the whole reason the obvious version fails.
- `NO_SAFE_MATCH` is a real answer, not a failure to answer. It means
  nothing here has been proven for your case — so solve it fresh, and do
  not treat a pattern you recall as verified.

**Run builds through it, not around it.** `run_observed_command` instead of
running the command yourself: `npm test`, `npm run build`, `pnpm build`,
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

**Contribute what you learned.** If a search MISSED, and you then worked
out the answer and your build passed, call `propose_public_sample` with the
goal, the packages and the symbols. Do it while you still remember what you
first got wrong — that mistake is the most valuable part, because it is
what the next agent will get wrong too, and five minutes later it is gone.
Publishing still needs the human's explicit approval at the CLI; proposing
costs you nothing and loses nothing if they decline.
