# Issue #433 P0 deployment safety cross-validation

Frozen implementation baseline: PR #446 commit
`56ae2166cc0e45add213690a9bf46f7d92df9c6d`.

Frozen independent audit: Opus-high commit
`c30e6c2`, `DEPLOY_SAFETY_AUDIT.md`.

This matrix was written only after the independent report was committed. The
implementation worker was not shown the audit before that freeze.

| Claim | Classification | Cross-validation decision |
|---|---|---|
| The empty-`application_name` row from the recorded original server network was rejected instead of entering the existing exact server-backend cleanup path. | Confirmed | Run artifact `10416862861` and both code readings agree. PR #446's network + container lifetime + PID/backend-start reclassification is narrowly safe. |
| A truly foreign client can make forward cleanup fail and then make `finalize()` fail at the same check before `rollback-server.sh`. | Confirmed | Direct control-flow inspection agrees with Opus. The original PR does not fix this broader availability failure. Forward activation must stay strict; recovery must record but never signal the foreign row and must restore the exact previous server after owned helper/backend/DDL hazards are proved absent. |
| The termination polling loops can consume the whole grace period in their condition and raise without a post-terminate observation. | Confirmed | The deadline is evaluated only after the blocking poll. The loop must keep a bounded wall-clock ceiling while guaranteeing one observation after the signal. |
| Recovery necessarily needs at least 13 psql round trips and therefore necessarily exceeds 60 seconds at 10.28 seconds each. | Needs Evidence | The arithmetic is a useful stress lead, but branch-dependent early exits and the observed 12.632-second recovery cleanup do not prove the universal count. Generic recovery timeouts must not be treated as advisory without persisted proof that helper/backend/DDL hazards are absent. |
| Production server connections have an empty application name. | Confirmed | Compose and Go connection setup set no name, and multiple run artifacts show empty server-backend names. A stable name improves diagnostics but cannot become an ownership requirement because the previous deployed server lacks it. |
| The exact process responsible for PID 430949 is known. | Needs Evidence | Network/lifetime fields make it eligible for the server cleanup classifier, but the retained artifact cannot distinguish a late original-server query from every possible address-lifetime race. The fix is required to be safe under either interpretation. |
| A post-migration cleanup failure omits the new ledger head from evidence. | Confirmed | `migrationLedger` is currently persisted after helper cleanup; the run artifact has only `migrationLedgerBefore` even though migration completed. |
| `rollbackServerBackends` deduplication is stable. | Confirmed defect | Whole-dictionary equality includes changing query fields; dedupe should use `(pid, backendStart)`, matching helper backend evidence. |
| All cleanup failures should allow rollback. | False Positive / rejected recommendation | Owned helper/backend survival or active index DDL is not safe to wave through. Only the explicitly proven unowned-client terminal condition may be advisory during recovery; other failures remain fail-closed. |

## Required merge bar

PR #446 must retain its exact original-server classifier and add regression
coverage proving:

1. forward activation still rejects an unclassified client;
2. the unclassified client is never cancelled or terminated;
3. recovery still runs the exact rollback scripts once owned helper/backend and
   DDL absence are proved;
4. owned helper/backend survival and active DDL still block rollback;
5. termination gets a real bounded post-signal observation;
6. the post-migration ledger and backend identity evidence stay bounded and
   privacy-safe.

Canonical CI, a safe production deployment retry, exact live revision proof,
and public/admin/Farm after-probes remain separate acceptance requirements.
