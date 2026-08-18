-- Retire, but do not delete, legacy matrix wishes that no public worker can
-- prepare exactly. Historical rows used partial {os} or
-- {runtime,runtimeMajor} shapes; exposing them ahead of exact Java jobs caused
-- permanent queue starvation. Keeping the rows as done preserves the audit
-- trail while ensuring only closed-schema WorkerRequirements are claimable.
UPDATE verification_jobs
SET status = 'done', claimed_by = NULL, claimed_at = NULL
WHERE reason = 'matrix'
  AND status IN ('open', 'claimed')
  AND (
    jsonb_typeof(want_env) IS DISTINCT FROM 'object'
    OR NOT (
      want_env ? 'sandboxCapability'
      AND want_env ? 'verifierAdapter'
      AND want_env ? 'ecosystem'
      AND want_env ? 'runtime'
      AND want_env ? 'runtimeVersion'
      AND want_env ? 'executionContext'
    )
    OR (want_env - 'sandboxCapability' - 'verifierAdapter' - 'ecosystem'
                 - 'runtime' - 'runtimeVersion' - 'executionContext') <> '{}'::jsonb
    OR want_env->>'sandboxCapability' IS DISTINCT FROM 'CONTAINER_RUN'
    OR want_env->>'ecosystem' IS DISTINCT FROM 'maven'
    OR want_env->>'runtime' IS DISTINCT FROM 'java'
    OR want_env->>'executionContext' IS DISTINCT FROM 'java'
    OR want_env->>'verifierAdapter' IS NULL
    OR want_env->>'verifierAdapter' NOT IN ('maven-java@1', 'gradle-java@1')
    OR want_env->>'runtimeVersion' IS NULL
    OR want_env->>'runtimeVersion' NOT IN ('8', '11', '17', '21', '25')
  );
