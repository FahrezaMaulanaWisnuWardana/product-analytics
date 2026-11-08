CREATE OR REPLACE FUNCTION enforce_project_collection_limit()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_count bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext(NEW.project_id));
    IF TG_TABLE_NAME = 'event_definitions' AND EXISTS (
        SELECT 1 FROM event_definitions WHERE project_id = NEW.project_id AND event = NEW.event
    ) THEN
        RETURN NEW;
    END IF;
    IF TG_TABLE_NAME = 'alerts' AND EXISTS (
        SELECT 1 FROM alerts WHERE id = NEW.id
    ) THEN
        RETURN NEW;
    END IF;
    EXECUTE format('SELECT count(*) FROM %I WHERE project_id = $1', TG_TABLE_NAME)
        INTO current_count
        USING NEW.project_id;
    IF current_count >= 1000 THEN
        RAISE EXCEPTION '% has reached the per-project limit of 1000 rows', TG_TABLE_NAME
            USING ERRCODE = 'program_limit_exceeded';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER saved_reports_project_limit BEFORE INSERT ON saved_reports
FOR EACH ROW EXECUTE FUNCTION enforce_project_collection_limit();
CREATE TRIGGER dashboards_project_limit BEFORE INSERT ON dashboards
FOR EACH ROW EXECUTE FUNCTION enforce_project_collection_limit();
CREATE TRIGGER annotations_project_limit BEFORE INSERT ON annotations
FOR EACH ROW EXECUTE FUNCTION enforce_project_collection_limit();
CREATE TRIGGER event_definitions_project_limit BEFORE INSERT ON event_definitions
FOR EACH ROW EXECUTE FUNCTION enforce_project_collection_limit();
CREATE TRIGGER alerts_project_limit BEFORE INSERT ON alerts
FOR EACH ROW EXECUTE FUNCTION enforce_project_collection_limit();
