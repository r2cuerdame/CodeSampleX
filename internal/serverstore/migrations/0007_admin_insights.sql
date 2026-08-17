-- The private dashboard reads only the most recent 30 days of receipt
-- activity. Keep that bounded window index-backed as the network grows.
CREATE INDEX IF NOT EXISTS receipts_created_result_idx
ON receipts (created_at DESC, contract_result) INCLUDE (sample_id);
