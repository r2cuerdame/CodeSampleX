-- Fast lookup for direct evidence in authoring coverage and backlog queries.
-- 0001 only indexed (purl, symbol); finding direct evidence rows required
-- fetching table heap tuples across hundreds of thousands of rows.
CREATE INDEX IF NOT EXISTS evidence_agg_direct_purl_idx
  ON evidence_agg (purl) WHERE direct;
