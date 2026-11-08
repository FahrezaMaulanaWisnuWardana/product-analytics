-- Project deletion is a supported lifecycle operation. The original schema
-- predates that workflow and leaves events attached with a restrictive foreign
-- key, which makes every non-empty project impossible to delete.
ALTER TABLE events
    DROP CONSTRAINT events_project_id_fkey,
    ADD CONSTRAINT events_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE;
