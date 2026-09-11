WITH bounds AS MATERIALIZED (
  SELECT statement_timestamp() AS window_end,
         statement_timestamp() - interval '1 hour' AS window_start
), slot_keys(slot, label_sha256) AS (VALUES
  (1, '__SLOT1_HASH__'), (2, '__SLOT2_HASH__'), (3, '__SLOT3_HASH__')
), node_receipts AS MATERIALIZED (
  SELECT r.receipt_id, r.sample_id, r.contract_result
    FROM receipts r CROSS JOIN bounds b
   WHERE r.created_at >= b.window_start AND r.created_at < b.window_end
     AND r.peer_id = '__NODE_PEER__'
), candidates AS MATERIALIZED (
  SELECT DISTINCT sample_id FROM node_receipts WHERE contract_result = 'PASS'
), first_times AS MATERIALIZED (
  -- Restrict the sample set, never its receipt history or peer set.
  SELECT c.sample_id, min(r.created_at) AS first_at,
         count(*) FILTER (WHERE r.created_at IS NULL) AS unknown_times
    FROM candidates c JOIN receipts r ON r.sample_id = c.sample_id
   WHERE r.contract_result = 'PASS'
   GROUP BY c.sample_id
), first_peers AS MATERIALIZED (
  SELECT f.sample_id, count(DISTINCT r.peer_id) AS peers,
         bool_or(r.peer_id = '__NODE_PEER__') AS includes_node
    FROM first_times f JOIN receipts r
      ON r.sample_id = f.sample_id AND r.created_at = f.first_at
     AND r.contract_result = 'PASS'
    CROSS JOIN bounds b
   WHERE f.unknown_times = 0
     AND f.first_at >= b.window_start AND f.first_at < b.window_end
   GROUP BY f.sample_id
), window_drafts AS MATERIALIZED (
  SELECT d.sample_id, d.worker_label, d.created_at, d.updated_at
    FROM authoring_drafts d CROSS JOIN bounds b
   -- updated_at >= created_at is a table CHECK. This redundant lower bound
   -- can use the existing updated index. An upper updated bound would lose
   -- drafts edited after the creation window.
   WHERE d.updated_at >= b.window_start
     AND d.created_at >= b.window_start AND d.created_at < b.window_end
), slot_counts AS MATERIALIZED (
  SELECT k.slot, count(DISTINCT d.sample_id) AS drafts,
         count(DISTINCT d.sample_id) FILTER (WHERE d.created_at = d.updated_at) AS equal_times,
         count(DISTINCT d.sample_id) FILTER (WHERE d.updated_at > d.created_at) AS changed_times
    FROM slot_keys k LEFT JOIN window_drafts d
      ON encode(sha256(convert_to(d.worker_label, 'UTF8')), 'hex') = k.label_sha256
   GROUP BY k.slot
)
SELECT json_build_object(
  'windowStart', to_char(b.window_start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
  'windowEnd', to_char(b.window_end AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
  'receipts', json_build_object(
    'accepted', (SELECT count(DISTINCT receipt_id) FROM node_receipts),
    'pass', (SELECT count(DISTINCT receipt_id) FROM node_receipts WHERE contract_result = 'PASS')),
  'sampleFirstPass', json_build_object(
    'unambiguousNodeSamples', (SELECT count(*) FROM first_peers WHERE includes_node AND peers = 1),
    'sharedFirstTimestampSamples', (SELECT count(*) FROM first_peers WHERE includes_node AND peers > 1),
    'unknownHistoricalTimestampSamples', (SELECT count(*) FROM first_times WHERE unknown_times > 0)),
  'gen', json_build_object(
    'currentSlotAttributedDrafts', (SELECT sum(drafts) FROM slot_counts),
    'createdAndUpdatedTimestampEqual', (SELECT sum(equal_times) FROM slot_counts),
    'postcreationTimestampChanged', (SELECT sum(changed_times) FROM slot_counts),
    'slots', (SELECT json_agg(json_build_object(
      'slot', slot, 'currentSlotAttributedDrafts', drafts,
      'createdAndUpdatedTimestampEqual', equal_times,
      'postcreationTimestampChanged', changed_times) ORDER BY slot) FROM slot_counts)))
FROM bounds b
