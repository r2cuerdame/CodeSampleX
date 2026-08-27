-- Keep the package coordinates carried by each immutable sample in a small
-- relational projection. Wanted reads used to parse every manifest JSON array
-- on every request, so a quiet crawler or one farm poll could consume the
-- production CPU baseline even though the underlying corpus was unchanged.

CREATE TABLE sample_packages(
  sample_id TEXT NOT NULL REFERENCES samples(sample_id) ON DELETE CASCADE,
  purl TEXT NOT NULL,
  coord TEXT NOT NULL,
  PRIMARY KEY(sample_id, purl));

CREATE INDEX sample_packages_coord_idx ON sample_packages(coord, sample_id);

INSERT INTO sample_packages(sample_id, purl, coord)
SELECT s.sample_id,
       package.value,
       left(package.value,
            length(package.value) - strpos(reverse(package.value), '@') + 1)
  FROM samples s
  CROSS JOIN LATERAL jsonb_array_elements_text(
    CASE WHEN jsonb_typeof(s.manifest->'packages') = 'array'
         THEN s.manifest->'packages' ELSE '[]'::jsonb END
  ) AS package(value)
 WHERE strpos(reverse(package.value), '@') > 0
ON CONFLICT DO NOTHING;
