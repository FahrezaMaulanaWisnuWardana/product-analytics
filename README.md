# Pulse

An independent product analytics service written in Go, backed by PostgreSQL. Connect multiple products to one instance, follow what people do, and use conversion and retention data to decide what to improve. The dashboard and browser SDK are embedded into a single Go binary.

## Start locally

Requires **Go 1.25+** and **PostgreSQL 17+**. No Node.js build is needed.

```sh
# Use an installed PostgreSQL distribution. Initializes only this project's cluster.
make db
make run
```

`make db` creates a private `.env` with random credentials if one does not already exist, starts a loopback-only PostgreSQL instance on port 55433, and creates database `product_analytics`. It finds PostgreSQL tools on PATH or the installed Homebrew PostgreSQL 17 location. Data lives in **`.postgres/`**; this directory and `.env` are ignored by Git. `make db-stop` stops this project's cluster. Start it again with `make db`; existing data is retained. Do not run the native database and the Docker database on the same port. The native helper is for local development and initializes a local superuser; use a dedicated least-privilege role for hosted deployments.

Open **http://127.0.0.1:8080** and sign in with `PULSE_ADMIN_TOKEN` from `.env`. On an empty database, the welcome page walks you through signing in, creating a project, and connecting tracking. Existing projects skip the welcome page, including after restarts and across browsers.

`make run`, `make demo`, and `make migrate` load `.env` through `scripts/run.sh`. A directly launched Go binary reads environment variables, not dotenv files:

```sh
export DATABASE_URL='postgres://product_analytics:YOUR_PASSWORD@127.0.0.1:55433/product_analytics?sslmode=disable'
export PULSE_ADMIN_TOKEN='YOUR_LONG_RANDOM_ADMIN_TOKEN'
go run ./cmd/pulse
```

`DATABASE_URL` and `PULSE_ADMIN_TOKEN` are required. There is no file-storage fallback. The previous `--data` option has been removed. The HTTP service refuses to start without an explicit administrator token and never prints a generated administrator credential to logs.

Existing installations may continue using the former `PRODUCT_ANALYTICS_*` server variables while migrating; `PULSE_*` variables take precedence when both are set.

Use `make demo` to seed 60 days of sample activity **only when there are no projects**. Do not seed a database that you plan to import existing data into.

## Docker Compose

```sh
# If make db has not already generated .env:
cp .env.example .env
# Replace the database-password and admin-token placeholders in .env.
# DATABASE_URL must use the same password when you also run Go on the host.
docker compose up -d --build
```

Compose starts PostgreSQL and Pulse, waits for database readiness, and persists PostgreSQL data in the **`product_analytics_postgres` named volume** (Compose adds its project-name prefix). Both published ports bind to localhost. The app connects to the `db` service over the Compose network. `docker compose down` preserves the volume; deleting the volume deletes its database. The Compose runtime requires a running Docker daemon.

To run Go on the host with a Docker database instead:

```sh
docker compose up -d db
make run
```

Do not use an existing `.env` with placeholder credentials outside local development. Secure cookies default to true; set `PULSE_SECURE_COOKIES=false` only for direct loopback development. When false, administrator login and authenticated API access require both a loopback peer connection and a loopback request host. For remote access, put a TLS reverse proxy in front of Pulse, preserve the original Host header, and keep secure cookies enabled. Use verified TLS for a remote PostgreSQL connection; `sslmode=disable` in the examples is for local development.

## Self-Hosting and Production Deployment

The service compiles down to a single binary with all dashboard assets and the client SDK embedded. For public or remote deployments, place Pulse behind a **TLS reverse proxy** (e.g., Caddy or Nginx) and use verified TLS for PostgreSQL.

### 1. Production Docker Compose

1. **Generate secure random credentials:**
   ```sh
   DB_PASSWORD=$(openssl rand -hex 32)
   ADMIN_TOKEN=$(openssl rand -hex 32)

   cat <<EOF > .env
   POSTGRES_PASSWORD=${DB_PASSWORD}
   PULSE_ADMIN_TOKEN=${ADMIN_TOKEN}
   POSTGRES_PORT=55433
   PULSE_PORT=8080
   PULSE_SECURE_COOKIES=true
   PULSE_MAX_EVENTS_PER_PROJECT=1000000
   PULSE_MAX_QUERY_EVENTS=100000
   DATABASE_URL=postgres://product_analytics:${DB_PASSWORD}@db:5432/product_analytics?sslmode=disable
   EOF
   ```
   > **Note:** Store `PULSE_ADMIN_TOKEN` securely in your password manager. It is required to log in to the dashboard.

2. **Start the containers:**
   ```sh
   docker compose up -d --build
   ```
   PostgreSQL schema migrations apply automatically on startup under an advisory lock.

### 2. Standalone Binary (Systemd)

To run the binary directly on a Linux VPS (Ubuntu/Debian) or with a managed PostgreSQL database (e.g., AWS RDS, Supabase):

1. **Build the binary and create a dedicated system user:**
   ```sh
   make build
   sudo mv bin/pulse /usr/local/bin/
   sudo useradd -r -s /bin/false pulse
   ```

2. **Create the systemd service file** at `/etc/systemd/system/pulse.service`:
   ```ini
   [Unit]
   Description=Pulse Analytics Service
   After=network.target postgresql.service

   [Service]
   Type=simple
   User=pulse
   Group=pulse
   Environment="DATABASE_URL=postgres://product_analytics:YOUR_PASSWORD@127.0.0.1:5432/product_analytics?sslmode=verify-full"
   Environment="PULSE_ADMIN_TOKEN=YOUR_ADMIN_TOKEN"
   Environment="PULSE_SECURE_COOKIES=true"
   Environment="PULSE_MAX_EVENTS_PER_PROJECT=1000000"
   Environment="PULSE_MAX_QUERY_EVENTS=100000"
   ExecStart=/usr/local/bin/pulse --addr 127.0.0.1:8080
   Restart=always
   RestartSec=5s
   NoNewPrivileges=true
   ProtectSystem=full
   ProtectHome=true

   [Install]
   WantedBy=multi-user.target
   ```

3. **Enable and start the service:**
   ```sh
   sudo systemctl daemon-reload
   sudo systemctl enable --now pulse
   ```

### 3. Reverse Proxy Configuration

The application validates the `Origin` header against the request's `Host` header to protect administrator sessions. Your reverse proxy **must preserve the original `Host` header** and terminate TLS.

#### Caddy (Automated TLS)
```caddyfile
analytics.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

#### Nginx
```nginx
server {
    server_name analytics.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
    }

    listen 443 ssl;
    ssl_certificate /etc/letsencrypt/live/analytics.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/analytics.example.com/privkey.pem;
}
```

### 4. Configuration Reference

| Environment Variable | Default | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | *Required* | PostgreSQL connection string (`postgres://user:pass@host:port/db?sslmode=...`). |
| `PULSE_ADMIN_TOKEN` | *Required* | Bearer token for administrator authentication and dashboard access. |
| `PULSE_SECURE_COOKIES` | `true` | Enforces `Secure` flag on cookies. Set `false` only on loopback development. |
| `PULSE_MAX_EVENTS_PER_PROJECT` | `1000000` | Maximum stored events per project before rejecting writes with HTTP 429. |
| `PULSE_MAX_QUERY_EVENTS` | `100000` | Upper limit on events loaded into memory per query (returns HTTP 413 if exceeded). |

### 5. Production Operations

* **Health checks:** Monitor `/healthz` for process liveness and `/readyz` for database connectivity.
* **Database backups:** Back up PostgreSQL regularly using `pg_dump`:
  ```sh
  # When using Docker Compose:
  docker compose exec -t db pg_dump -U product_analytics product_analytics > backup.sql
  ```
* **Upgrades:** Pull the latest code and rebuild. Database schema migrations execute idempotently inside a locked transaction upon process startup.

## Database

On startup, Pulse automatically applies embedded SQL migrations from `internal/analytics/migrations/`. Migrations run in a transaction under a PostgreSQL advisory lock, so concurrent app startups do not apply the same migration twice. `make migrate` applies migrations without starting HTTP.

| Table | Stored data |
| --- | --- |
| `projects` | Project IDs, names, public ingestion keys, allowed origins (`jsonb`), creation time, stored-event count |
| `events` | Project, event ID/name, person/session IDs, timestamp, properties (`jsonb`) |
| `schema_migrations` | Applied migration versions and timestamps |

The primary key `(project_id, id)` deduplicates events per product, across restarts and concurrent app instances. Foreign keys prevent orphan events. Indexes cover project/time, person/time, and event-name/time. Event batches and their project counters commit atomically. A transactionally enforced per-project quota defaults to 1,000,000 stored events and serializes concurrent writers across app instances. Every analytics read comes from PostgreSQL, so one app instance can immediately read events written by another.

Open database `product_analytics` in a PostgreSQL client using the connection information in `.env`, or inspect it with `psql`. For example:

```sql
SELECT id, name, created_at FROM projects ORDER BY created_at;
SELECT project_id, count(*) FROM events GROUP BY project_id;
SELECT event, count(*) FROM events GROUP BY event ORDER BY count(*) DESC;
SELECT version, applied_at FROM schema_migrations ORDER BY version;
```

Back up PostgreSQL with your normal database backup tooling, such as `pg_dump`. The environment file contains credentials, not event data. Analytics calculations load selected events into Go, and retention reads prior event history, but each query is capped at 100,000 events by default to bound process memory. For larger volumes, move aggregation and retention computation into SQL or an analytics database and add pagination. Automatic expiry, deletion, and compaction policies are not implemented.

Capacity controls are configured with `PULSE_MAX_EVENTS_PER_PROJECT` and `PULSE_MAX_QUERY_EVENTS`. Both must be positive integers. Increase them only with matching PostgreSQL storage, process memory, ingress budgets, monitoring, and recovery capacity. The storage quota is enforced transactionally across concurrent app instances; requests beyond it return HTTP 429. Analytics queries beyond the in-memory limit return HTTP 413 and should be narrowed.

## Import your existing JSONL data

Stop writers to the old file-backed installation before importing. Start the new database, then run:

```sh
make db
./scripts/run.sh --import-jsonl ./data/events.jsonl
# If you also used the former --data ./fresh-data option:
./scripts/run.sh --import-jsonl ./fresh-data/events.jsonl
make run
```

Import runs as a command and exits; it does not start the HTTP server. The importer **never edits or deletes the source file**. It retains original project IDs, ingestion keys, allowed origins, event IDs, properties, and timestamps. Multiple project records in a legacy file resolve to that file's latest version. Repeat imports deduplicate events and preserve database project edits. A project ID with a different ingestion key is rejected, and malformed records, key conflicts, or orphan events roll back the import instead of leaving partial data. Back up files before migration and compare event counts afterward. This importer reads the legacy file into memory, so migrate very large files using a purpose-built streaming process.

After import, product integrations keep using the same project keys and `/capture` endpoint. New writes go only to PostgreSQL; legacy JSONL files are no longer updated. To preview first-install onboarding, point `DATABASE_URL` at a separate empty database rather than removing your existing data.

## What works

- Multiple products with isolated events, people, and analytics.
- HTTP ingestion: batches of up to 100, 1 MB requests, per-project event deduplication, allowed-origin checks, and future-timestamp validation.
- Dashboard with users, events, sessions, page views, trends, comparisons, top events, popular pages, and recent activity.
- Searchable event explorer, captured properties, user timelines, and a 15-second refresh.
- Configurable ordered funnels with a 7-day conversion window and drop-off guidance.
- Daily retention cohorts based on each person's first event across the full project history.
- People explorer, JSON export, setup guide, browser SDK, and Go client.
- Token-protected administrator APIs, HTTP-only SameSite session cookies, same-origin write checks, and security headers.

## Connect a browser product

Create a project and allow exact origins, such as `https://app.example.com` and `http://localhost:3000`, without paths or a trailing slash. Add:

```html
<script src="https://analytics.example.com/sdk.js"></script>
<script>
  pulse.init('pk_YOUR_PROJECT_KEY', { host: 'https://analytics.example.com' });
  pulse.identify('user_123', { plan: 'Pro' });
  pulse.capture('project_created', { source: 'onboarding' });
</script>
```

The SDK tracks the initial page view; call `pulse.page()` after SPA navigation. It buffers events and flushes every 5 seconds or at 20 events. It uses local storage for the distinct ID, session storage for sessions (30-minute inactivity window), and no cookies. It retries network/server failures in memory, bounds the queue at 500, and attempts a beacon on page exit. Delivery is best effort. It respects Do Not Track by default. `pulse.optOut()` clears the queue and disables collection; `pulse.optIn()` reenables it. Call `pulse.reset()` on logout. Configure your product's CSP to allow scripts and connections to your analytics host.

`identify` changes the identity for future events and emits `$identify`; it does not retroactively merge anonymous activity. People properties reflect their most recent event. Do not send sensitive data you do not need. Public ingestion keys are write-only, not secrets or proof of a trusted sender. The administrator token grants access to all projects on the instance.

## Send backend events

Go, Laravel, Node, Python, and other services use the same API:

```sh
curl http://127.0.0.1:8080/capture \
  -H 'Content-Type: application/json' \
  -d '{
    "api_key": "pk_YOUR_PROJECT_KEY",
    "events": [{
      "id": "order-123-paid",
      "event": "subscription_started",
      "distinct_id": "user_123",
      "session_id": "session_456",
      "properties": {"plan": "Pro", "amount": 29}
    }]
  }'
```

Returns HTTP 202 with `{"accepted":1}`. Repeating the same ID for the same project returns `{"accepted":0}`. Missing IDs and timestamps are generated by the server. Use RFC 3339 timestamps for historical events. Backend clients omit `Origin`; browser origins must match the allowlist. Set rate limits and an ingestion budget at the reverse proxy before accepting public traffic.

The [Go client](sdk/go/client.go) supports contexts, batches, a 10-second timeout, and stable IDs. It does not retry automatically; reuse an event ID on application retries. The module is named `pulse` locally; change `go.mod` and imports to your real repository path before distributing it across repositories.

```go
client, err := pulse.New("https://analytics.example.com", "pk_YOUR_KEY")
if err != nil { return err }
err = client.Capture(ctx, pulse.Event{
    ID: "order-123-paid", Name: "subscription_started", DistinctID: "user_123",
    Properties: map[string]any{"plan": "Pro"},
})
```

## API

Administrator endpoints require `Authorization: Bearer <PULSE_ADMIN_TOKEN>` or a dashboard session cookie. Query endpoints take `project=<id>` and optionally `from=YYYY-MM-DD&to=YYYY-MM-DD`. End dates are inclusive, today is partial, and ranges are limited to 366 days. All analytics day boundaries are UTC.

| Method | Route | Purpose |
| --- | --- | --- |
| GET | `/healthz` | Process liveness |
| GET | `/readyz` | PostgreSQL readiness (503 on failure) |
| GET | `/api/installation` | Public first-install status, no project details |
| GET | `/sdk.js` | Browser SDK |
| POST | `/capture` | Public project-key ingestion |
| POST | `/api/login` | Exchange `{"token":"..."}` for an administrator cookie |
| POST | `/api/logout` | Clear the cookie |
| GET | `/api/projects` | List projects and ingestion keys |
| POST | `/api/projects` | Create with `{"name":"...","origins":["https://..."]}` |
| PATCH | `/api/projects` | Update with `{"id":"prj_...","name":"...","origins":[...]}` |
| GET | `/api/overview` | Metrics, previous period, daily series, breakdowns, recent events |
| GET | `/api/events` | Latest 500 matches; filters: `event`, `person`, `q` |
| GET | `/api/people` | People active in the selected period |
| GET | `/api/funnel` | Repeat `step` 2–8 times for ordered conversion |
| GET | `/api/retention` | Daily cohorts with day 0 through day 7 retention |
| GET | `/api/export` | All events for the project and date range |

Funnel users count once per step; qualifying events must be in range and complete in order within seven days of step one. Sessions are distinct `(distinct_id, session_id)` pairs, excluding missing session IDs. Users are distinct IDs, not verified people. Retention measures exact-day returns. Database read failures return 503 rather than empty analytics or a new-install status.

## Validate and build

```sh
go test ./...                  # Pure unit/client tests; database tests skip without TEST_DATABASE_URL.
go vet ./...
node --test web/sdk.test.cjs    # Optional browser SDK checks; requires Node.js.
make build
```

Run integration and race tests against a **disposable** PostgreSQL database:

```sh
TEST_DATABASE_URL='postgres://test_user:password@127.0.0.1:5432/product_analytics_test?sslmode=disable' \
  make integration-test
```

Tests create uniquely named schemas and drop only those schemas afterward. The test user needs permission to create schemas. Tests cover migrations, persisted data after reconnects, project isolation, concurrent duplicate ingestion, transaction rollback, repeatable legacy import, database failures, and first-install onboarding state. Docker Compose needs separate verification when the Docker daemon is available.

Team accounts/roles, session replay, feature flags, automatic identity merging, and saved reports are not implemented.
