CREATE TABLE IF NOT EXISTS admin_sessions (
    id text PRIMARY KEY,
    token_hash text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
