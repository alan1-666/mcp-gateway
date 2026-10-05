-- Support workspace-scoped keyset traversal. The partial index serves the
-- published catalog for every role; substring filtering remains a SQL predicate.
CREATE INDEX tools_workspace_created_idx
    ON tools (workspace_id,created_at DESC,id DESC);

CREATE INDEX tools_catalog_created_idx
    ON tools (workspace_id,created_at DESC,id DESC)
    WHERE status='published' AND enabled;
