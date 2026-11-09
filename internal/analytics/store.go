package analytics

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Project struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Key           string    `json:"key"`
	Origins       []string  `json:"origins"`
	CreatedAt     time.Time `json:"created_at"`
	RetentionDays int       `json:"retention_days"`
}
type Event struct {
	ID         string         `json:"id"`
	ProjectID  string         `json:"project_id"`
	Name       string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	SessionID  string         `json:"session_id"`
	Timestamp  time.Time      `json:"timestamp"`
	Properties map[string]any `json:"properties"`
}

const (
	DefaultMaxEventsPerProject = 1_000_000
	DefaultMaxQueryEvents      = 100_000
	MaxEventPropertiesBytes    = 64 * 1024
	MaxQueryBytes              = 32 * 1024 * 1024
	MaxProjects                = 100
	MaxAccessTokens            = 100
	MaxProjectOrigins          = 100
	MaxProjectOriginsBytes     = 64 * 1024
	DefaultAuditRetentionDays  = 365
	DefaultTokenRetentionDays  = 90
)

type Store struct {
	db                  *sql.DB
	maxEventsPerProject int
	maxQueryEvents      int
}

var (
	ErrProjectNotFound = errors.New("project not found")
	ErrEventQuota      = errors.New("project event quota reached")
	ErrQueryEventLimit = errors.New("query event limit reached")
	ErrEventBytesLimit = errors.New("event properties byte limit reached")
	ErrQueryBytesLimit = errors.New("query byte limit reached")
	ErrWorkspaceLimit  = errors.New("workspace capacity limit reached")
)

func mapWorkspaceLimit(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "P0001" {
		return fmt.Errorf("%w: %s", ErrWorkspaceLimit, pgErr.Message)
	}
	return err
}

//go:embed migrations/*.sql
var migrations embed.FS

func ID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func timeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}
func Open(databaseURL string) (*Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("DATABASE_URL is required; start PostgreSQL with docker compose up -d db and set DATABASE_URL")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("configure PostgreSQL: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := timeout()
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	s := &Store{db: db, maxEventsPerProject: DefaultMaxEventsPerProject, maxQueryEvents: DefaultMaxQueryEvents}
	if err = s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) SetLimits(maxEventsPerProject, maxQueryEvents int) error {
	if maxEventsPerProject < 1 || maxQueryEvents < 1 {
		return errors.New("event and query limits must be positive")
	}
	s.maxEventsPerProject = maxEventsPerProject
	s.maxQueryEvents = maxQueryEvents
	return nil
}
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize schema changes across instances, on the same transaction/connection.
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(734927018)"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, file := range files {
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", file.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", file.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", file.Name()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type scanner interface{ Scan(...any) error }

const projectColumns = "id,name,api_key,origins,created_at,retention_days"

func scanProject(row scanner) (Project, error) {
	var p Project
	var origins []byte
	err := row.Scan(&p.ID, &p.Name, &p.Key, &origins, &p.CreatedAt, &p.RetentionDays)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(origins, &p.Origins)
	return p, err
}
func (s *Store) Projects() ([]Project, error) {
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, "SELECT "+projectColumns+" FROM projects ORDER BY created_at,id LIMIT $1", MaxProjects+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
		if len(out) > MaxProjects {
			return nil, fmt.Errorf("%w: projects exceed %d records", ErrWorkspaceLimit, MaxProjects)
		}
	}
	return out, rows.Err()
}
func (s *Store) HasProjects() (bool, error) {
	ctx, cancel := timeout()
	defer cancel()
	var exists bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM projects LIMIT 1)").Scan(&exists)
	return exists, err
}
func (s *Store) find(column, value string) (Project, bool, error) {
	ctx, cancel := timeout()
	defer cancel()
	p, err := scanProject(s.db.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE "+column+"=$1", value))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, false, nil
	}
	return p, err == nil, err
}
func (s *Store) Project(id string) (Project, bool, error) { return s.find("id", id) }
func (s *Store) ByKey(key string) (Project, bool, error)  { return s.find("api_key", key) }
func projectValues(name string, origins []string) (string, []byte, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return "", nil, errors.New("project name must contain 1–80 characters")
	}
	if origins == nil {
		origins = []string{}
	}
	b, err := json.Marshal(origins)
	if err == nil && (len(origins) > MaxProjectOrigins || len(b) > MaxProjectOriginsBytes) {
		return "", nil, errors.New("project origins exceed the workspace limit")
	}
	return name, b, err
}
func (s *Store) CreateProject(name string, origins []string) (Project, error) {
	name, b, err := projectValues(name, origins)
	if err != nil {
		return Project{}, err
	}
	ctx, cancel := timeout()
	defer cancel()
	p, err := scanProject(s.db.QueryRowContext(ctx, "INSERT INTO projects(id,name,api_key,origins,created_at) VALUES($1,$2,$3,$4,$5) RETURNING "+projectColumns, ID("prj_"), name, ID("pk_"), b, time.Now().UTC()))
	return p, mapWorkspaceLimit(err)
}
func (s *Store) UpdateProject(id, name string, origins []string) (Project, error) {
	name, b, err := projectValues(name, origins)
	if err != nil {
		return Project{}, err
	}
	ctx, cancel := timeout()
	defer cancel()
	p, err := scanProject(s.db.QueryRowContext(ctx, "UPDATE projects SET name=$2,origins=$3 WHERE id=$1 RETURNING "+projectColumns, id, name, b))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrProjectNotFound
	}
	return p, err
}
func (s *Store) Add(e Event) (bool, error) { n, err := s.AddBatch([]Event{e}); return n > 0, err }

// AddBatch commits all newly accepted events atomically. Database uniqueness
// deduplicates retries across concurrent writers and application instances.
func (s *Store) AddBatch(events []Event) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	ctx, cancel := timeout()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n, err := insertEvents(ctx, tx, events, s.maxEventsPerProject)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}
func insertEvents(ctx context.Context, tx *sql.Tx, events []Event, maxEventsPerProject int) (int, error) {
	// Consistent lock ordering prevents deadlocks for overlapping concurrent batches.
	ordered := append([]Event{}, events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ProjectID == ordered[j].ProjectID {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].ProjectID < ordered[j].ProjectID
	})
	current := map[string]int64{}
	for _, e := range ordered {
		if _, seen := current[e.ProjectID]; seen {
			continue
		}
		var eventCount int64
		if err := tx.QueryRowContext(ctx, "SELECT event_count FROM projects WHERE id=$1 FOR UPDATE", e.ProjectID).Scan(&eventCount); err != nil {
			return 0, err
		}
		current[e.ProjectID] = eventCount
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO events(project_id,id,event,distinct_id,session_id,occurred_at,properties) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(project_id,id) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	count := 0
	for _, e := range ordered {
		props := e.Properties
		if props == nil {
			props = map[string]any{}
		}
		b, err := json.Marshal(props)
		if err != nil {
			return 0, err
		}
		if len(b) > MaxEventPropertiesBytes {
			return 0, fmt.Errorf("%w: event %s exceeds %d bytes", ErrEventBytesLimit, e.ID, MaxEventPropertiesBytes)
		}
		res, err := stmt.ExecContext(ctx, e.ProjectID, e.ID, e.Name, e.DistinctID, e.SessionID, e.Timestamp, b)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		added := int(n)
		count += added
		current[e.ProjectID] += int64(added)
		if current[e.ProjectID] > int64(maxEventsPerProject) {
			return 0, fmt.Errorf("%w: project %s exceeds %d events", ErrEventQuota, e.ProjectID, maxEventsPerProject)
		}
	}
	for projectID, eventCount := range current {
		if _, err := tx.ExecContext(ctx, "UPDATE projects SET event_count=$2 WHERE id=$1", projectID, eventCount); err != nil {
			return 0, err
		}
	}
	return count, nil
}

// FunnelSuggestions searches a bounded sample of recent project activity.
func (s *Store) FunnelSuggestions(project, kind, search string) ([]string, error) {
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT value FROM (SELECT CASE WHEN $2 = 'page' THEN CASE WHEN event = '$pageview' THEN properties->>'$pathname' END ELSE event END AS value FROM events WHERE project_id=$1 ORDER BY occurred_at DESC LIMIT 10000) recent WHERE value IS NOT NULL AND value <> '' AND length(value) <= 500 AND position(lower($3) in lower(value)) > 0 ORDER BY value LIMIT 50`, project, kind, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) Events(project string, from, to time.Time) ([]Event, error) {
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,event,distinct_id,session_id,occurred_at,properties FROM events WHERE project_id=$1 AND occurred_at >= $2 AND occurred_at < $3 ORDER BY occurred_at,id LIMIT $4`, project, from, to, s.maxQueryEvents+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	totalBytes := 0
	for rows.Next() {
		var e Event
		var props []byte
		if err = rows.Scan(&e.ID, &e.ProjectID, &e.Name, &e.DistinctID, &e.SessionID, &e.Timestamp, &props); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(props, &e.Properties); err != nil {
			return nil, err
		}
		totalBytes += len(props) + len(e.ID) + len(e.ProjectID) + len(e.Name) + len(e.DistinctID) + len(e.SessionID)
		if totalBytes > MaxQueryBytes {
			return nil, fmt.Errorf("%w: narrow the query below %d bytes", ErrQueryBytesLimit, MaxQueryBytes)
		}
		out = append(out, e)
		if len(out) > s.maxQueryEvents {
			return nil, fmt.Errorf("%w: narrow the query below %d events", ErrQueryEventLimit, s.maxQueryEvents)
		}
	}
	return out, rows.Err()
}
