-- Admin request-coverage reads begin with the retained UTC-day demand ledger.
-- Its primary key begins with the coordinate, so a date-only predicate would
-- otherwise walk the full lifetime ledger before applying the seven-day bound.
-- Keep the read index-only and do not add any new identity or request payload.
CREATE INDEX IF NOT EXISTS wanted_dedup_epoch_coordinate_idx
ON wanted_dedup(epoch DESC, ecosystem, name, version, symbol, target_os);
