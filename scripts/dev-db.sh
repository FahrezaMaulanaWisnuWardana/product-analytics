#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

if command -v pg_ctl >/dev/null 2>&1; then
  pg_bin="$(dirname "$(command -v pg_ctl)")"
elif [[ -x /opt/homebrew/opt/postgresql@17/bin/pg_ctl ]]; then
  pg_bin=/opt/homebrew/opt/postgresql@17/bin
else
  echo 'PostgreSQL tools were not found. Install PostgreSQL or use docker compose up -d db.' >&2
  exit 1
fi

if [[ ! -f .env ]]; then
  umask 077
  db_password="$(openssl rand -hex 32)"
  admin_token="$(openssl rand -hex 32)"
  cat > .env <<CONFIG
POSTGRES_PASSWORD=$db_password
PULSE_ADMIN_TOKEN=$admin_token
POSTGRES_PORT=55433
PULSE_PORT=8080
PULSE_SECURE_COOKIES=false
PULSE_MAX_EVENTS_PER_PROJECT=1000000
PULSE_MAX_QUERY_EVENTS=100000
DATABASE_URL=postgres://product_analytics:$db_password@127.0.0.1:55433/product_analytics?sslmode=disable
CONFIG
  echo 'Created .env with random local database and administrator credentials.'
fi
set -a
# Local development configuration; keep this file private and out of Git.
source .env
set +a
: "${POSTGRES_PASSWORD:?Set POSTGRES_PASSWORD in .env}"
: "${POSTGRES_PORT:=55433}"
: "${DATABASE_URL:?Set DATABASE_URL in .env}"
[[ "$POSTGRES_PORT" =~ ^[0-9]+$ ]] || { echo 'POSTGRES_PORT must be a number.' >&2; exit 1; }
cluster="$PWD/.postgres"
case "${1:-start}" in
 start)
  if [[ ! -f "$cluster/PG_VERSION" ]]; then
    password_file="$(mktemp)"
    chmod 600 "$password_file"
    trap 'rm -f "$password_file"' EXIT
    printf '%s\n' "$POSTGRES_PASSWORD" > "$password_file"
    "$pg_bin/initdb" -D "$cluster" -U product_analytics --auth-local=trust --auth-host=scram-sha-256 --pwfile="$password_file" --no-instructions
    rm -f "$password_file"
    trap - EXIT
  fi
  if ! "$pg_bin/pg_ctl" -D "$cluster" status >/dev/null 2>&1; then
    "$pg_bin/pg_ctl" -D "$cluster" -l "$cluster/server.log" -o "-h 127.0.0.1 -p $POSTGRES_PORT -c unix_socket_directories='' -c shared_buffers=32MB -c max_connections=50" -w start
  fi
  export PGPASSWORD="$POSTGRES_PASSWORD"
  exists="$("$pg_bin/psql" -h 127.0.0.1 -p "$POSTGRES_PORT" -U product_analytics -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='product_analytics'")"
  if [[ "$exists" != 1 ]]; then
    "$pg_bin/createdb" -h 127.0.0.1 -p "$POSTGRES_PORT" -U product_analytics product_analytics
  fi
  echo "PostgreSQL is ready on 127.0.0.1:$POSTGRES_PORT (database: product_analytics). Run make run."
  ;;
 stop) "$pg_bin/pg_ctl" -D "$cluster" -m fast -w stop ;;
 status) "$pg_bin/pg_ctl" -D "$cluster" status ;;
 *) echo 'Usage: scripts/dev-db.sh [start|stop|status]' >&2; exit 1 ;;
esac
