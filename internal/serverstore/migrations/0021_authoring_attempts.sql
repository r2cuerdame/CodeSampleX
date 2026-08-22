-- A bounded memory of what happened the last times each public coordinate was
-- handed to a sample writer, and the record of the ones that stopped being
-- offered.
--
-- Enumerating impossible package shapes by hand always lags the ecosystem.
-- Three were found that way — the Gradle plugin marker, the pom-only BOM or
-- parent, the per-platform npm native binary — and each cost a worker hours
-- before anybody noticed. What generalises is not the shape of the package but
-- the attempt: a coordinate that keeps being handed out and keeps producing
-- nothing.
--
-- Nothing here deletes. A coordinate is WITHHELD, with its reason, evidence and
-- age beside it, and either an operator or a timer puts it back.
--
-- The state travels as one JSONB document rather than a column per counter
-- because the transition rules live in Go and are shared byte for byte with the
-- in-memory store. Expressing them twice — once here, once there — is exactly
-- how the two implementations of the farm panel drifted apart before.
CREATE TABLE authoring_attempts(
  ecosystem TEXT NOT NULL,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  symbol TEXT NOT NULL,
  ledger JSONB NOT NULL,
  quarantined_at TIMESTAMPTZ,
  reopens_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY(ecosystem, name, version, symbol));

-- The operations panel reads only the withheld rows, and the picker asks about
-- one coordinate at a time through the primary key.
CREATE INDEX authoring_attempts_withheld_idx
ON authoring_attempts(quarantined_at DESC)
WHERE quarantined_at IS NOT NULL;
