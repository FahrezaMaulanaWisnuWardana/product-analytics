package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	MaxCollaborationRecords   = 1000
	MaxCollaborationJSONBytes = 64 * 1024
)

var ErrCollaborationLimit = errors.New("collaboration collection limit reached")

func collectionLimitError(kind string) error {
	return fmt.Errorf("%w: %s exceeds %d records", ErrCollaborationLimit, kind, MaxCollaborationRecords)
}

type AccessToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type SavedReport struct {
	ID         string         `json:"id"`
	ProjectID  string         `json:"project_id"`
	Name       string         `json:"name"`
	ReportType string         `json:"report_type"`
	Config     map[string]any `json:"config"`
	CreatedBy  string         `json:"created_by"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type Dashboard struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	ReportIDs []string  `json:"report_ids"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Annotation struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	OccurredAt  time.Time `json:"occurred_at"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type EventDefinitionRecord struct {
	ProjectID          string            `json:"project_id"`
	Event              string            `json:"event"`
	DisplayName        string            `json:"display_name"`
	Description        string            `json:"description"`
	Owner              string            `json:"owner"`
	Status             string            `json:"status"`
	ExpectedProperties map[string]string `json:"expected_properties"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

type Alert struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	Name       string     `json:"name"`
	Metric     string     `json:"metric"`
	Comparison string     `json:"comparison"`
	Threshold  float64    `json:"threshold"`
	Enabled    bool       `json:"enabled"`
	LastValue  *float64   `json:"last_value,omitempty"`
	LastStatus string     `json:"last_status"`
	CheckedAt  *time.Time `json:"checked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type AuditEntry struct {
	ID        int64          `json:"id"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	ProjectID string         `json:"project_id,omitempty"`
	TargetID  string         `json:"target_id,omitempty"`
	Details   map[string]any `json:"details"`
	CreatedAt time.Time      `json:"created_at"`
}

func validRole(role string) bool { return role == "viewer" || role == "editor" || role == "owner" }

func (s *Store) CreateAccessToken(name, role, hash string) (AccessToken, error) {
	name, role = strings.TrimSpace(name), strings.TrimSpace(role)
	if name == "" || len(name) > 80 || !validRole(role) || hash == "" {
		return AccessToken{}, errors.New("token needs a name and viewer, editor, or owner role")
	}
	ctx, cancel := timeout()
	defer cancel()
	var out AccessToken
	err := s.db.QueryRowContext(ctx, `INSERT INTO access_tokens(id,name,token_hash,role) VALUES($1,$2,$3,$4) RETURNING id,name,role,created_at,last_used_at,revoked_at`, ID("tok_"), name, hash, role).Scan(&out.ID, &out.Name, &out.Role, &out.CreatedAt, &out.LastUsedAt, &out.RevokedAt)
	return out, mapWorkspaceLimit(err)
}

func (s *Store) AuthenticateAccessToken(hash string) (AccessToken, bool, error) {
	ctx, cancel := timeout()
	defer cancel()
	var out AccessToken
	err := s.db.QueryRowContext(ctx, `UPDATE access_tokens SET last_used_at=now() WHERE token_hash=$1 AND revoked_at IS NULL RETURNING id,name,role,created_at,last_used_at,revoked_at`, hash).Scan(&out.ID, &out.Name, &out.Role, &out.CreatedAt, &out.LastUsedAt, &out.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessToken{}, false, nil
	}
	return out, err == nil, err
}

func (s *Store) AccessTokens() ([]AccessToken, error) {
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,role,created_at,last_used_at,revoked_at FROM access_tokens ORDER BY created_at DESC LIMIT $1`, MaxAccessTokens+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessToken{}
	for rows.Next() {
		var item AccessToken
		if err := rows.Scan(&item.ID, &item.Name, &item.Role, &item.CreatedAt, &item.LastUsedAt, &item.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
		if len(out) > MaxAccessTokens {
			return nil, fmt.Errorf("%w: access tokens exceed %d records", ErrWorkspaceLimit, MaxAccessTokens)
		}
	}
	return out, rows.Err()
}

func (s *Store) RevokeAccessToken(id string) error {
	ctx, cancel := timeout()
	defer cancel()
	res, err := s.db.ExecContext(ctx, `UPDATE access_tokens SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func jsonObject(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	return json.Marshal(value)
}

func scanReport(row scanner) (SavedReport, error) {
	var item SavedReport
	var raw []byte
	err := row.Scan(&item.ID, &item.ProjectID, &item.Name, &item.ReportType, &raw, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(raw, &item.Config)
	}
	return item, err
}

func (s *Store) SaveReport(project, name, reportType, actor string, config map[string]any) (SavedReport, error) {
	name, reportType = strings.TrimSpace(name), strings.TrimSpace(reportType)
	if name == "" || len(name) > 120 || reportType == "" || len(reportType) > 40 {
		return SavedReport{}, errors.New("invalid report")
	}
	raw, err := jsonObject(config)
	if err != nil {
		return SavedReport{}, err
	}
	if len(raw) > MaxCollaborationJSONBytes {
		return SavedReport{}, errors.New("report config is too large")
	}
	ctx, cancel := timeout()
	defer cancel()
	return scanReport(s.db.QueryRowContext(ctx, `INSERT INTO saved_reports(id,project_id,name,report_type,config,created_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,project_id,name,report_type,config,created_by,created_at,updated_at`, ID("rpt_"), project, name, reportType, raw, actor))
}

func (s *Store) Reports(project string) ([]SavedReport, error) {
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,name,report_type,config,created_by,created_at,updated_at FROM saved_reports WHERE project_id=$1 ORDER BY updated_at DESC LIMIT $2`, project, MaxCollaborationRecords+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedReport{}
	for rows.Next() {
		item, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
		if len(out) > MaxCollaborationRecords {
			return nil, collectionLimitError("reports")
		}
	}
	return out, rows.Err()
}

func (s *Store) UpdateReport(project, id, name, reportType string, config map[string]any) (SavedReport, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return SavedReport{}, errors.New("invalid report name")
	}
	raw, err := jsonObject(config)
	if err != nil {
		return SavedReport{}, err
	}
	if len(raw) > MaxCollaborationJSONBytes {
		return SavedReport{}, errors.New("report config is too large")
	}
	ctx, cancel := timeout()
	defer cancel()
	return scanReport(s.db.QueryRowContext(ctx, `UPDATE saved_reports SET name=$3,config=$4,updated_at=now() WHERE project_id=$1 AND id=$2 AND report_type=$5 RETURNING id,project_id,name,report_type,config,created_by,created_at,updated_at`, project, id, name, raw, reportType))
}

func (s *Store) DeleteReport(project, id string) error {
	ctx, cancel := timeout()
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM saved_reports WHERE project_id=$1 AND id=$2`, project, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SaveDashboard(project, name, actor string, reportIDs []string) (Dashboard, error) {
	if strings.TrimSpace(name) == "" || len(name) > 120 {
		return Dashboard{}, errors.New("invalid dashboard")
	}
	if reportIDs == nil {
		reportIDs = []string{}
	}
	raw, err := json.Marshal(reportIDs)
	if err != nil {
		return Dashboard{}, err
	}
	if len(reportIDs) > 200 || len(raw) > MaxCollaborationJSONBytes {
		return Dashboard{}, errors.New("dashboard contains too many reports")
	}
	ctx, cancel := timeout()
	defer cancel()
	var out Dashboard
	var ids []byte
	err = s.db.QueryRowContext(ctx, `INSERT INTO dashboards(id,project_id,name,report_ids,created_by) VALUES($1,$2,$3,$4,$5) RETURNING id,project_id,name,report_ids,created_by,created_at,updated_at`, ID("dsh_"), project, name, raw, actor).Scan(&out.ID, &out.ProjectID, &out.Name, &ids, &out.CreatedBy, &out.CreatedAt, &out.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(ids, &out.ReportIDs)
	}
	return out, err
}

func (s *Store) Dashboards(project string) ([]Dashboard, error) {
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,name,report_ids,created_by,created_at,updated_at FROM dashboards WHERE project_id=$1 ORDER BY updated_at DESC LIMIT $2`, project, MaxCollaborationRecords+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Dashboard{}
	for rows.Next() {
		var item Dashboard
		var raw []byte
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Name, &raw, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item.ReportIDs); err != nil {
			return nil, err
		}
		out = append(out, item)
		if len(out) > MaxCollaborationRecords {
			return nil, collectionLimitError("dashboards")
		}
	}
	return out, rows.Err()
}

func (s *Store) DeleteDashboard(project, id string) error {
	ctx, cancel := timeout()
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM dashboards WHERE project_id=$1 AND id=$2`, project, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) AddAnnotation(project, title, description, actor string, occurred time.Time) (Annotation, error) {
	title, description = strings.TrimSpace(title), strings.TrimSpace(description)
	if title == "" || len(title) > 120 || len(description) > 2000 || occurred.IsZero() {
		return Annotation{}, errors.New("invalid annotation")
	}
	ctx, cancel := timeout()
	defer cancel()
	var out Annotation
	err := s.db.QueryRowContext(ctx, `INSERT INTO annotations(id,project_id,title,description,occurred_at,created_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,project_id,title,description,occurred_at,created_by,created_at`, ID("ann_"), project, title, description, occurred, actor).Scan(&out.ID, &out.ProjectID, &out.Title, &out.Description, &out.OccurredAt, &out.CreatedBy, &out.CreatedAt)
	return out, err
}

func (s *Store) Annotations(project string, from, to time.Time) ([]Annotation, error) {
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,title,description,occurred_at,created_by,created_at FROM annotations WHERE project_id=$1 AND occurred_at >= $2 AND occurred_at < $3 ORDER BY occurred_at DESC LIMIT $4`, project, from, to, MaxCollaborationRecords+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Annotation{}
	for rows.Next() {
		var item Annotation
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Title, &item.Description, &item.OccurredAt, &item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
		if len(out) > MaxCollaborationRecords {
			return nil, collectionLimitError("annotations")
		}
	}
	return out, rows.Err()
}

func (s *Store) DeleteAnnotation(project, id string) error {
	ctx, cancel := timeout()
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM annotations WHERE project_id=$1 AND id=$2`, project, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) EventDefinitions(project string) ([]EventDefinitionRecord, error) {
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT project_id,event,display_name,description,owner,status,expected_properties,updated_at FROM event_definitions WHERE project_id=$1 ORDER BY event LIMIT $2`, project, MaxCollaborationRecords+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventDefinitionRecord{}
	for rows.Next() {
		var item EventDefinitionRecord
		var raw []byte
		if err := rows.Scan(&item.ProjectID, &item.Event, &item.DisplayName, &item.Description, &item.Owner, &item.Status, &raw, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item.ExpectedProperties); err != nil {
			return nil, err
		}
		out = append(out, item)
		if len(out) > MaxCollaborationRecords {
			return nil, collectionLimitError("event definitions")
		}
	}
	return out, rows.Err()
}

func (s *Store) UpsertEventDefinition(item EventDefinitionRecord) (EventDefinitionRecord, error) {
	if item.ProjectID == "" || strings.TrimSpace(item.Event) == "" || len(item.Event) > 200 {
		return item, errors.New("invalid event definition")
	}
	if item.Status == "" {
		item.Status = "unverified"
	}
	if item.Status != "unverified" && item.Status != "verified" && item.Status != "hidden" {
		return item, errors.New("invalid status")
	}
	if item.ExpectedProperties == nil {
		item.ExpectedProperties = map[string]string{}
	}
	raw, err := json.Marshal(item.ExpectedProperties)
	if err != nil {
		return item, err
	}
	if len(item.DisplayName) > 120 || len(item.Owner) > 120 || len(raw) > MaxCollaborationJSONBytes {
		return item, errors.New("event definition is too large")
	}
	ctx, cancel := timeout()
	defer cancel()
	var saved EventDefinitionRecord
	var savedRaw []byte
	err = s.db.QueryRowContext(ctx, `INSERT INTO event_definitions(project_id,event,display_name,description,owner,status,expected_properties,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,now()) ON CONFLICT(project_id,event) DO UPDATE SET display_name=EXCLUDED.display_name,description=EXCLUDED.description,owner=EXCLUDED.owner,status=EXCLUDED.status,expected_properties=EXCLUDED.expected_properties,updated_at=now() RETURNING project_id,event,display_name,description,owner,status,expected_properties,updated_at`, item.ProjectID, item.Event, item.DisplayName, item.Description, item.Owner, item.Status, raw).Scan(&saved.ProjectID, &saved.Event, &saved.DisplayName, &saved.Description, &saved.Owner, &saved.Status, &savedRaw, &saved.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(savedRaw, &saved.ExpectedProperties)
	}
	return saved, err
}

func (s *Store) SaveAlert(item Alert) (Alert, error) {
	if item.ProjectID == "" || strings.TrimSpace(item.Name) == "" {
		return item, errors.New("invalid alert")
	}
	ctx, cancel := timeout()
	defer cancel()
	if item.ID == "" {
		item.ID = ID("alt_")
	}
	err := s.db.QueryRowContext(ctx, `INSERT INTO alerts(id,project_id,name,metric,comparison,threshold,enabled) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,metric=EXCLUDED.metric,comparison=EXCLUDED.comparison,threshold=EXCLUDED.threshold,enabled=EXCLUDED.enabled WHERE alerts.project_id=EXCLUDED.project_id RETURNING id,project_id,name,metric,comparison,threshold,enabled,last_value,last_status,checked_at,created_at`, item.ID, item.ProjectID, item.Name, item.Metric, item.Comparison, item.Threshold, item.Enabled).Scan(&item.ID, &item.ProjectID, &item.Name, &item.Metric, &item.Comparison, &item.Threshold, &item.Enabled, &item.LastValue, &item.LastStatus, &item.CheckedAt, &item.CreatedAt)
	return item, err
}

func (s *Store) Alerts(project string) ([]Alert, error) {
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,name,metric,comparison,threshold,enabled,last_value,last_status,checked_at,created_at FROM alerts WHERE project_id=$1 ORDER BY created_at DESC LIMIT $2`, project, MaxCollaborationRecords+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var item Alert
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Name, &item.Metric, &item.Comparison, &item.Threshold, &item.Enabled, &item.LastValue, &item.LastStatus, &item.CheckedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
		if len(out) > MaxCollaborationRecords {
			return nil, collectionLimitError("alerts")
		}
	}
	return out, rows.Err()
}

func (s *Store) DeleteAlert(project, id string) error {
	ctx, cancel := timeout()
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM alerts WHERE project_id=$1 AND id=$2`, project, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateRetention(project string, days int) (Project, error) {
	if days < 1 || days > 3650 {
		return Project{}, errors.New("retention must be between 1 and 3650 days")
	}
	ctx, cancel := timeout()
	defer cancel()
	return scanProject(s.db.QueryRowContext(ctx, `UPDATE projects SET retention_days=$2 WHERE id=$1 RETURNING `+projectColumns, project, days))
}

func (s *Store) RotateProjectKey(project string) (Project, error) {
	ctx, cancel := timeout()
	defer cancel()
	return scanProject(s.db.QueryRowContext(ctx, `UPDATE projects SET api_key=$2 WHERE id=$1 RETURNING `+projectColumns, project, ID("pk_")))
}

func (s *Store) PurgeExpired(project string) (int64, error) {
	ctx, cancel := timeout()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM events USING projects WHERE events.project_id=projects.id AND projects.id=$1 AND events.occurred_at < now() - make_interval(days => projects.retention_days)`, project)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET event_count=(SELECT count(*) FROM events WHERE project_id=$1) WHERE id=$1`, project); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

func (s *Store) PurgeAllExpired() (int64, error) {
	ctx, cancel := timeout()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM events USING projects WHERE events.project_id=projects.id AND events.occurred_at < now() - make_interval(days => projects.retention_days)`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET event_count=(SELECT count(*) FROM events WHERE events.project_id=projects.id)`); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

func (s *Store) DeletePerson(project, person string) (int64, error) {
	ctx, cancel := timeout()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `WITH RECURSIVE edges AS (
		SELECT DISTINCT distinct_id AS identified_id, properties->>'$anon_distinct_id' AS anonymous_id
		FROM events
		WHERE project_id=$1 AND event='$identify' AND COALESCE(properties->>'$anon_distinct_id','') <> ''
	), linked(id) AS (
		SELECT $2::text
		UNION
		SELECT CASE WHEN edges.anonymous_id=linked.id THEN edges.identified_id ELSE edges.anonymous_id END
		FROM linked JOIN edges ON edges.anonymous_id=linked.id OR edges.identified_id=linked.id
	)
	DELETE FROM events WHERE project_id=$1 AND distinct_id IN (SELECT id FROM linked)`, project, person)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET event_count=(SELECT count(*) FROM events WHERE project_id=$1) WHERE id=$1`, project); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

func (s *Store) DeleteProject(project string) error {
	ctx, cancel := timeout()
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id=$1`, project)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

func (s *Store) Audit(actor, action, project, target string, details map[string]any) error {
	raw, err := jsonObject(details)
	if err != nil {
		return err
	}
	ctx, cancel := timeout()
	defer cancel()
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_log(actor,action,project_id,target_id,details) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5)`, actor, action, project, target, raw)
	return err
}

func (s *Store) AuditLog(limit int) ([]AuditEntry, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	ctx, cancel := timeout()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id,actor,action,COALESCE(project_id,''),COALESCE(target_id,''),details,created_at FROM audit_log ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var item AuditEntry
		var raw []byte
		if err := rows.Scan(&item.ID, &item.Actor, &item.Action, &item.ProjectID, &item.TargetID, &raw, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item.Details); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) PurgeWorkspaceHistory(auditBefore, revokedTokenBefore time.Time) (int64, int64, error) {
	ctx, cancel := timeout()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	auditResult, err := tx.ExecContext(ctx, `DELETE FROM audit_log WHERE created_at < $1`, auditBefore)
	if err != nil {
		return 0, 0, err
	}
	tokenResult, err := tx.ExecContext(ctx, `DELETE FROM access_tokens WHERE revoked_at IS NOT NULL AND revoked_at < $1`, revokedTokenBefore)
	if err != nil {
		return 0, 0, err
	}
	auditDeleted, err := auditResult.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	tokensDeleted, err := tokenResult.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	return auditDeleted, tokensDeleted, tx.Commit()
}

func (s *Store) ProjectEventCount(ctx context.Context, project string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT event_count FROM projects WHERE id=$1`, project).Scan(&n)
	return n, err
}

func wrapNotFound(kind string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s not found", kind)
	}
	return err
}
