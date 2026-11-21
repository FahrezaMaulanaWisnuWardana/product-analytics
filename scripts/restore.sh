#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

# Local development configuration; keep .env private and out of Git.
if [[ -f .env ]]; then
  set -a
  source .env
  set +a
fi

usage() {
  echo 'Usage: scripts/restore.sh <backup-file> RESTORE' >&2
  echo 'RESTORE must be passed explicitly because this overwrites database objects.' >&2
}

if [[ $# -ne 2 || "${2:-}" != "RESTORE" ]]; then
  usage
  exit 1
fi

file="$1"
if [[ ! -f "$file" ]]; then
  echo "Backup file not found: $file" >&2
  exit 1
fi

if ! command -v pg_restore >/dev/null 2>&1; then
  echo 'pg_restore not found. Install PostgreSQL client tools (e.g. postgresql@17).' >&2
  exit 1
fi

# Same credential handling as backup.sh: credentials are never echoed.
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

# One transaction plus fail-fast so a partial restore is rolled back.
restore_db() {
  pg_restore --format=custom --clean --if-exists --no-owner --no-acl \
    --single-transaction --exit-on-error "$@" "$file"
}
if [[ -n "$conn" ]]; then
  restore_db --dbname="$conn"
else
  restore_db
fi
echo "Restore completed from: $file"
