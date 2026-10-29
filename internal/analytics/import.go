package analytics

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

type ImportResult struct {
	Projects int `json:"projects"`
	Events   int `json:"events"`
}

// ImportJSONL reads a legacy file without modifying it. Existing matching projects
// are preserved; a key collision aborts the entire import rather than mixing data.
func (s *Store) ImportJSONL(reader io.Reader) (ImportResult, error) {
	scan := bufio.NewScanner(reader)
	scan.Buffer(make([]byte, 64*1024), 2*1024*1024)
	projects := map[string]Project{}
	events := []Event{}
	line := 0
	for scan.Scan() {
		line++
		var r struct {
			Project *Project `json:"project"`
			Event   *Event   `json:"event"`
		}
		if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
			return ImportResult{}, fmt.Errorf("legacy record %d: %w", line, err)
		}
		if (r.Project == nil) == (r.Event == nil) {
			return ImportResult{}, fmt.Errorf("legacy record %d: expected exactly one project or event", line)
		}
		if r.Project != nil {
			p := *r.Project
			if p.ID == "" || p.Key == "" || p.CreatedAt.IsZero() {
				return ImportResult{}, fmt.Errorf("legacy record %d: invalid project", line)
			}
			projects[p.ID] = p
		}
		if r.Event != nil {
			e := *r.Event
			if e.ID == "" || e.ProjectID == "" || e.Timestamp.IsZero() {
				return ImportResult{}, fmt.Errorf("legacy record %d: invalid event", line)
			}
			events = append(events, e)
		}
	}
	if err := scan.Err(); err != nil {
		return ImportResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer tx.Rollback()
	ids := []string{}
	for id := range projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := ImportResult{}
	for _, id := range ids {
		p := projects[id]
		name, origins, err := projectValues(p.Name, p.Origins)
		if err != nil {
			return ImportResult{}, err
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO projects(id,name,api_key,origins,created_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING", p.ID, name, p.Key, origins, p.CreatedAt)
		if err != nil {
			return ImportResult{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return ImportResult{}, err
		}
		result.Projects += int(n)
		var key string
		if err = tx.QueryRowContext(ctx, "SELECT api_key FROM projects WHERE id=$1", p.ID).Scan(&key); err != nil {
			return ImportResult{}, err
		}
		if key != p.Key {
			return ImportResult{}, fmt.Errorf("project %s has a different ingestion key in the database", p.ID)
		}
	}
	result.Events, err = insertEvents(ctx, tx, events, s.maxEventsPerProject)
	if err != nil {
		return ImportResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return ImportResult{}, err
	}
	return result, nil
}
