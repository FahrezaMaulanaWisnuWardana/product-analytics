CREATE OR REPLACE FUNCTION enforce_workspace_collection_limit()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_count bigint;
    maximum_count bigint := TG_ARGV[0]::bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(1469598104);
    EXECUTE format('SELECT count(*) FROM %I', TG_TABLE_NAME) INTO current_count;
    IF current_count >= maximum_count THEN
        RAISE EXCEPTION '% has reached the workspace limit of % rows', TG_TABLE_NAME, maximum_count;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER projects_workspace_limit BEFORE INSERT ON projects
FOR EACH ROW EXECUTE FUNCTION enforce_workspace_collection_limit('100');

CREATE TRIGGER access_tokens_workspace_limit BEFORE INSERT ON access_tokens
FOR EACH ROW EXECUTE FUNCTION enforce_workspace_collection_limit('100');
