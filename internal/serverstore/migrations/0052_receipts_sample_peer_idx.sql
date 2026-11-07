-- The public verification queue excludes cross-jobs already judged by the
-- polling peer. Probe both keys together so that anti-join does not walk every
-- receipt for each queued sample as the receipt ledger grows.
CREATE INDEX IF NOT EXISTS receipts_sample_peer_idx
  ON receipts (sample_id, peer_id);
