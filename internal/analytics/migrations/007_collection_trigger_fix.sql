CREATE OR REPLACE FUNCTION enforce_project_collection_limit()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE current_count bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext(NEW.project_id));
    IF TG_TABLE_NAME = 'event_definitions' THEN
        IF EXISTS (SELECT 1 FROM event_definitions WHERE project_id = NEW.project_id AND event = NEW.event) THEN RETURN NEW; END IF;
    ELSIF TG_TABLE_NAME = 'alerts' THEN
        IF EXISTS (SELECT 1 FROM alerts WHERE id = NEW.id) THEN RETURN NEW; END IF;
    END IF;
    EXECUTE format('SELECT count(*) FROM %I WHERE project_id = $1', TG_TABLE_NAME)
        INTO current_count USING NEW.project_id;
    IF current_count >= 1000 THEN
        RAISE EXCEPTION '% has reached the per-project limit of 1000 rows', TG_TABLE_NAME USING ERRCODE = 'program_limit_exceeded';
    END IF;
    RETURN NEW;
END;
$$;
