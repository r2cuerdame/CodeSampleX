# Production staging budget evidence (#404)

Source: failed canonical [Production deploy 34831402445](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34831402445)
and retained artifact `10342780896`, observed 2026-09-14 UTC. The run targeted
released, CI-passed `c45f928d1ab14abc55d3641c08f72d848ef49ce2` from live
`dea13af9c0d5dd4ed1ef06b55f46f123be1c0069`, using operational controller
`b9d3af73d96671576b888b8c61494dd2b0cf2ea2`.

Staging started at 10:07:32.245Z. After shipping config and release artifacts,
the remote image tar copy alone took 17.344 seconds and ended at 10:11:23.280Z.
The controller then had about two seconds of its 240-second phase allowance
left. The bounded remote `docker load` command exceeded the roughly one-second
child allowance, so the fail-closed controller did not activate and retained
the owner lock. The phase recorded 232.093 seconds; this is a censored lower
bound, not a completed staging duration or percentile.

The artifact records `rollback=unverified`, `smoke=not-started`,
`deployedSha=servedRevision=dea13af...`, and image
`sha256:d9960286...`. A bounded owner check after the failed run found no
remaining `docker load` process. The original container still served v0.1.180
at its unchanged 08:42:32Z start; candidate config, exact rollback snapshots,
and `dist.rollback-promoted` remained. Docker image enumeration itself did not
finish within 12 seconds, so this evidence does not claim whether the candidate
image load completed.

The new 360-second staging ceiling adds 120 seconds to this measured censored
boundary while preserving every per-command timeout and unknown-completion
failure. The complete non-SQL failure envelope therefore becomes 1,460 seconds.
`ceil(M/60)+26` leaves the same 100-second runner/termination reserve, and the
job retains its separate three-minute checkout/artifact reserve.