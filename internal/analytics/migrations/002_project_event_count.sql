ALTER TABLE projects
    ADD COLUMN event_count bigint NOT NULL DEFAULT 0 CHECK (event_count >= 0);

UPDATE projects AS project
SET event_count = (
    SELECT count(*)
    FROM events AS event
    WHERE event.project_id = project.id
);
