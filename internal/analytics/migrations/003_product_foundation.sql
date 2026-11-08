ALTER TABLE projects
    ADD COLUMN retention_days integer NOT NULL DEFAULT 365 CHECK (retention_days BETWEEN 1 AND 3650);

CREATE TABLE access_tokens (
    id text PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    token_hash text NOT NULL UNIQUE,
    role text NOT NULL CHECK (role IN ('viewer', 'editor', 'owner')),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at timestamptz
);

CREATE TABLE saved_reports (
    id text PRIMARY KEY,
    project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    report_type text NOT NULL CHECK (length(report_type) BETWEEN 1 AND 40),
    config jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(config) = 'object'),
    created_by text NOT NULL DEFAULT 'owner',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX saved_reports_project_updated_idx ON saved_reports(project_id, updated_at DESC);

CREATE TABLE dashboards (
    id text PRIMARY KEY,
    project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    report_ids jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(report_ids) = 'array'),
    created_by text NOT NULL DEFAULT 'owner',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE annotations (
    id text PRIMARY KEY,
    project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    occurred_at timestamptz NOT NULL,
    created_by text NOT NULL DEFAULT 'owner',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX annotations_project_time_idx ON annotations(project_id, occurred_at DESC);

CREATE TABLE event_definitions (
    project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    event text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    owner text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'unverified' CHECK (status IN ('unverified', 'verified', 'hidden')),
    expected_properties jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(expected_properties) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(project_id, event)
);

CREATE TABLE alerts (
    id text PRIMARY KEY,
    project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    metric text NOT NULL CHECK (metric IN ('users', 'events', 'sessions', 'pageviews', 'conversion')),
    comparison text NOT NULL CHECK (comparison IN ('above', 'below', 'changes_by')),
    threshold double precision NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    last_value double precision,
    last_status text NOT NULL DEFAULT 'unknown' CHECK (last_status IN ('unknown', 'ok', 'triggered')),
    checked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
    id bigserial PRIMARY KEY,
    actor text NOT NULL,
    action text NOT NULL,
    project_id text,
    target_id text,
    details jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_created_idx ON audit_log(created_at DESC);
