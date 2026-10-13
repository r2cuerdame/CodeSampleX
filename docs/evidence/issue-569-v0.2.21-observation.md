# v0.2.21 production observation tracking (#569)

- Immutable release target: `db9f421a4b751176a023a9de163680985c47c493` (`v0.2.21`).
- Source: [merged PR #568](https://github.com/r2cuerdame/CodeSampleX/pull/568), whose merge commit is the target above.
- Deployment: [successful run 38467230901](https://github.com/r2cuerdame/CodeSampleX/actions/runs/38467230901). Its unmodified `production-evidence-38467230901` artifact records `targetSha` and `deployedSha` as the target above and `trackingIssue` as `568`.
- Automatic observation: [run 38467879871](https://github.com/r2cuerdame/CodeSampleX/actions/runs/38467879871) failed before evidence initialization because the receipt names a merged PR rather than a real issue.
- Manual observation: [run 38468825397](https://github.com/r2cuerdame/CodeSampleX/actions/runs/38468825397) started for deployment run 38467230901 after #569 was opened. The workflow's issue comment and observation artifact are the authoritative result when it finishes.

The existing `tracking_issue` correction accepts #569 for this deployment: the validator checked that #568 was merged as the exact deployed target and that #569 is a real issue. The original deployment receipt remains intact. No operating metrics or private request content are copied here.
