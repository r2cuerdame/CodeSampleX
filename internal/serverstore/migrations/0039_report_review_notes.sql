-- Keep the operator's evidence beside the immutable product-report decision.
ALTER TABLE csx_issue_reports ADD COLUMN review_note TEXT NOT NULL DEFAULT '';
