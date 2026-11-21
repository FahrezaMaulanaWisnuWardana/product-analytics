#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

# Local development configuration; keep .env private and out of Git.
if [[ -f .env ]]; then
  set -a
  source .env
  set +a
fi

if ! command -v pg_dump >/dev/null 2>&1; then
  echo 'pg_dump not found. Install PostgreSQL client tools (e.g. postgresql@17).' >&2
  exit 1
fi

# Credentials are never echoed. libpq does not read DATABASE_URL from the
# environment, so an explicit URL is passed as the conninfo argument; otherwise
# the password-only fallback uses PG* variables.
conn=""
if [[ -n "${DATABASE_URL:-}" ]]; then
  conn="$DATABASE_URL"
  unset PGHOST PGPORT PGUSER PGDATABASE PGPASSWORD
else
  : "${POSTGRES_PASSWORD:?Set DATABASE_URL or POSTGRES_PASSWORD in .env}"
  export PGPASSWORD="$POSTGRES_PASSWORD"
  export PGHOST="${PGHOST:-127.0.0.1}"
  export PGPORT="${POSTGRES_PORT:-55433}"
  export PGUSER="${PGUSER:-product_analytics}"
  export PGDATABASE="${PGDATABASE:-product_analytics}"
fi
export PGCONNECT_TIMEOUT="${PGCONNECT_TIMEOUT:-10}"

mkdir -p backups
chmod 700 backups
umask 077
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
file="backups/product_analytics_${stamp}.dump"

pg_dump --format=custom --no-owner --no-acl --file="$file" ${conn:+"$conn"}
echo "Backup created: $file"
