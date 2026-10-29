CREATE TABLE projects (
    id text PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    api_key text NOT NULL UNIQUE,
    origins jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(origins) = 'array'),
    created_at timestamptz NOT NULL
);
CREATE TABLE events (
    project_id text NOT NULL REFERENCES projects(id),
    id text NOT NULL,
    event text NOT NULL CHECK (length(event) BETWEEN 1 AND 200),
    distinct_id text NOT NULL CHECK (length(distinct_id) BETWEEN 1 AND 200),
    session_id text NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL,
    properties jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(properties) = 'object'),
    PRIMARY KEY (project_id, id)
);
CREATE INDEX events_project_time_idx ON events (project_id, occurred_at DESC);
CREATE INDEX events_project_person_time_idx ON events (project_id, distinct_id, occurred_at DESC);
CREATE INDEX events_project_name_time_idx ON events (project_id, event, occurred_at DESC);
