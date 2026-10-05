ALTER TABLE tools DROP CONSTRAINT tools_status_check;
ALTER TABLE tools ADD CONSTRAINT tools_status_check CHECK (status IN ('draft','published','retired'));

CREATE TABLE tool_versions (
 workspace_id text NOT NULL,
 tool_id text NOT NULL,
 version integer NOT NULL,
 definition jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace_id,tool_id,version),
 FOREIGN KEY(workspace_id,tool_id) REFERENCES tools(workspace_id,id) ON DELETE CASCADE
);
INSERT INTO tool_versions(workspace_id,tool_id,version,definition,created_at)
 SELECT workspace_id,id,version,definition,created_at FROM tools;

-- Every definition revision, including response-policy changes, is recorded in
-- the same transaction. Status/enable changes never rewrite historical data.
CREATE FUNCTION record_tool_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' THEN
  IF NEW.definition IS DISTINCT FROM OLD.definition AND NEW.version <= OLD.version THEN
   RAISE EXCEPTION 'tool definition changes require a new version';
  END IF;
  IF NEW.version < OLD.version THEN
   RAISE EXCEPTION 'tool versions cannot move backwards';
  END IF;
 END IF;
 IF TG_OP = 'INSERT' OR NEW.version IS DISTINCT FROM OLD.version THEN
  INSERT INTO tool_versions(workspace_id,tool_id,version,definition)
   VALUES(NEW.workspace_id,NEW.id,NEW.version,NEW.definition);
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER tools_record_version AFTER INSERT OR UPDATE ON tools
 FOR EACH ROW EXECUTE FUNCTION record_tool_version();

CREATE TABLE tool_candidates (
 workspace_id text NOT NULL,
 id text NOT NULL,
 tool_id text NOT NULL,
 base_version integer NOT NULL CHECK (base_version > 0),
 source_version integer,
 definition jsonb NOT NULL,
 reason text NOT NULL,
 created_by text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 published_version integer,
 published_at timestamptz,
 PRIMARY KEY(workspace_id,id),
 FOREIGN KEY(workspace_id,tool_id) REFERENCES tools(workspace_id,id) ON DELETE CASCADE,
 CHECK ((published_version IS NULL) = (published_at IS NULL))
);
CREATE INDEX tool_candidates_history_idx ON tool_candidates(workspace_id,tool_id,created_at DESC,id DESC);
