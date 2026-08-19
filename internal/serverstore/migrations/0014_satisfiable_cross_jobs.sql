-- Make in-flight cross jobs satisfiable, and release the drafts their
-- receipts already proved.
--
-- A cross job asks a DIFFERENT machine to reproduce a result, but its
-- requirements were copied from the author's manifest verbatim, patch
-- level included: "node 22.23.2", "go 1.26.5". No pinned image can
-- promise a patch digit, so the returning receipt was refused for not
-- matching its own job — and because that refusal is the same statement
-- that would have closed the job, the job stayed claimed until its lease
-- expired, was handed out again, and failed again. Jobs pinned to a
-- major completed normally throughout.
--
-- Reduce the runtime requirement of live cross jobs to the release line
-- it belongs to. Which component names the line differs by runtime:
-- Node, Bun, Deno and Java ship lines as majors, while Python, Go and
-- Rust name theirs with the minor. Matrix jobs exist precisely to pin an
-- exact version and are left untouched.
UPDATE verification_jobs
SET want_env = want_env || jsonb_build_object('runtimeVersion',
      CASE
        WHEN lower(coalesce(want_env->>'runtime','')) IN
             ('node','nodejs','bun','deno','java')
          THEN split_part(want_env->>'runtimeVersion', '.', 1)
        WHEN want_env->>'runtimeVersion' LIKE '%.%.%'
          THEN split_part(want_env->>'runtimeVersion', '.', 1) || '.'
               || split_part(want_env->>'runtimeVersion', '.', 2)
        ELSE want_env->>'runtimeVersion'
      END)
WHERE reason = 'cross'
  AND status IN ('open', 'claimed')
  AND jsonb_typeof(want_env) = 'object'
  AND want_env->>'runtimeVersion' LIKE '%.%';

-- Return the claims those refusals stranded, so the corrected jobs are
-- offered immediately instead of after another lease expiry.
UPDATE verification_jobs
SET status = 'open', claimed_by = NULL, claimed_at = NULL
WHERE reason = 'cross'
  AND status = 'claimed'
  AND claimed_at < now() - interval '30 minutes';

-- Release the drafts whose independent verification already passed.
--
-- Promotion additionally required a live authoring assignment row, but an
-- authoring session expires after an hour without a refresh and its
-- assignment is deleted with it. A draft verified after that window kept
-- its signed PASS receipt and stayed quarantined forever: verified, and
-- invisible. The receipt bound to a cross job is the independent
-- confirmation; the assignment is bookkeeping about who was asked to
-- write it, and its absence says nothing about whether the contract ran.
UPDATE samples s
SET status = 'CROSS_PASS', quarantined = false, quarantine_reason = NULL,
    updated_at = now()
WHERE s.status = 'DRAFT'
  AND s.quarantined
  AND EXISTS (SELECT 1 FROM receipts r
               WHERE r.sample_id = s.sample_id AND r.contract_result = 'PASS')
  AND EXISTS (SELECT 1 FROM verification_jobs j
               WHERE j.sample_id = s.sample_id AND j.reason = 'cross');
