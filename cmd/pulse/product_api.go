package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pulse/internal/analytics"
)

var reportViews = map[string]bool{"overview": true, "events": true, "funnels": true, "retention": true, "paths": true, "people": true}

func validReportConfig(reportType string, config map[string]any) bool {
	if !reportViews[reportType] || config == nil {
		return false
	}
	allowed := map[string]bool{"view": true, "days": true, "metric": true, "eventName": true, "steps": true, "property": true, "value": true, "version": true, "window_minutes": true}
	for key := range config {
		if !allowed[key] {
			return false
		}
	}
	if raw, exists := config["view"]; exists {
		view, ok := raw.(string)
		if !ok || view != reportType || !reportViews[view] {
			return false
		}
	}
	if raw, exists := config["metric"]; exists {
		metric, ok := raw.(string)
		if !ok || (metric != "users" && metric != "events") {
			return false
		}
	}
	if raw, exists := config["days"]; exists {
		days, ok := raw.(float64)
		if !ok || days < 1 || days > 366 || days != float64(int(days)) {
			return false
		}
	}
	for _, key := range []string{"eventName", "property", "value"} {
		if raw, exists := config[key]; exists {
			value, ok := raw.(string)
			if !ok || len(value) > 200 {
				return false
			}
		}
	}
	if raw, exists := config["steps"]; exists {
		steps, ok := raw.([]any)
		if !ok || len(steps) > 8 {
			return false
		}
		for _, step := range steps {
			if value, ok := step.(string); ok {
				if strings.TrimSpace(value) == "" || len(value) > 200 {
					return false
				}
				continue
			}
			item, ok := step.(map[string]any)
			if !ok || reportType != "funnels" || len(item) != 3 {
				return false
			}
			typ, a := item["type"].(string)
			op, b := item["operator"].(string)
			value, c := item["value"].(string)
			if !a || !b || !c || strings.TrimSpace(value) == "" || len(value) > 500 || (typ != "page" && typ != "event") || (op != "eq" && !(typ == "page" && op == "starts_with")) {
				return false
			}
		}
	}
	for _, key := range []string{"version", "window_minutes"} {
		if raw, exists := config[key]; exists {
			n, ok := raw.(float64)
			if !ok || n != float64(int(n)) || n < 1 || (key == "version" && n != 2) || (key == "window_minutes" && n > 525600) {
				return false
			}
		}
	}
	return true
}

func (s *server) productAPI(w http.ResponseWriter, r *http.Request) bool {
	actor := principalFrom(r)
	project := r.URL.Query().Get("project")
	switch r.URL.Path {
	case "/api/me":
		if r.Method != http.MethodGet {
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		} else {
			respond(w, http.StatusOK, actor)
		}
		return true
	case "/api/access-tokens":
		if !requireRole(w, r, "owner") {
			return true
		}
		s.accessTokenAPI(w, r, actor)
		return true
	case "/api/audit":
		if !requireRole(w, r, "owner") {
			return true
		}
		if r.Method != http.MethodGet {
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return true
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := s.store.AuditLog(limit)
		if err != nil {
			databaseError(w, err)
		} else {
			respond(w, http.StatusOK, items)
		}
		return true
	case "/api/reports":
		if !s.validProject(w, project) {
			return true
		}
		s.reportsAPI(w, r, actor, project)
		return true
	case "/api/dashboards":
		if !s.validProject(w, project) {
			return true
		}
		s.dashboardsAPI(w, r, actor, project)
		return true
	case "/api/annotations":
		if !s.validProject(w, project) {
			return true
		}
		s.annotationsAPI(w, r, actor, project)
		return true
	case "/api/event-definitions":
		if !s.validProject(w, project) {
			return true
		}
		s.eventDefinitionsAPI(w, r, actor, project)
		return true
	case "/api/alerts":
		if !s.validProject(w, project) {
			return true
		}
		s.alertsAPI(w, r, actor, project)
		return true
	case "/api/project-policy":
		if !s.validProject(w, project) {
			return true
		}
		s.projectPolicyAPI(w, r, actor, project)
		return true
	default:
		return false
	}
}

func (s *server) validProject(w http.ResponseWriter, id string) bool {
	if id == "" {
		fail(w, http.StatusBadRequest, "Project is required")
		return false
	}
	_, ok, err := s.store.Project(id)
	if err != nil {
		databaseError(w, err)
		return false
	}
	if !ok {
		fail(w, http.StatusNotFound, "Project not found")
		return false
	}
	return true
}

func storeError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, analytics.ErrProjectNotFound) {
		fail(w, http.StatusNotFound, "Resource not found")
		return
	}
	databaseError(w, err)
}

func (s *server) record(actor principal, action, project, target string, details map[string]any) {
	if err := s.store.Audit(actor.Name, action, project, target, details); err != nil {
		log.Printf("Audit write failed for %s: %v", action, err)
	}
}

func (s *server) accessTokenAPI(w http.ResponseWriter, r *http.Request, actor principal) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.AccessTokens()
		if err != nil {
			databaseError(w, err)
			return
		}
		respond(w, http.StatusOK, items)
	case http.MethodPost:
		var input struct{ Name, Role string }
		if decode(w, r, &input) != nil {
			fail(w, http.StatusBadRequest, "Invalid token")
			return
		}
		raw := analytics.ID("pulse_")
		item, err := s.store.CreateAccessToken(input.Name, input.Role, tokenHash(raw))
		if err != nil {
			if errors.Is(err, analytics.ErrWorkspaceLimit) {
				databaseError(w, err)
				return
			}
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(actor, "access_token.created", "", item.ID, map[string]any{"role": item.Role})
		respond(w, http.StatusCreated, map[string]any{"token": raw, "access": item})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if err := s.store.RevokeAccessToken(id); err != nil {
			storeError(w, err)
			return
		}
		s.record(actor, "access_token.revoked", "", id, nil)
		respond(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *server) reportsAPI(w http.ResponseWriter, r *http.Request, actor principal, project string) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.Reports(project)
		if err != nil {
			databaseError(w, err)
			return
		}
		respond(w, http.StatusOK, items)
	case http.MethodPost, http.MethodPatch:
		if !requireRole(w, r, "editor") {
			return
		}
		var input struct {
			ID     string         `json:"id"`
			Name   string         `json:"name"`
			Type   string         `json:"type"`
			Config map[string]any `json:"config"`
		}
		if decode(w, r, &input) != nil {
			fail(w, 400, "Invalid report")
			return
		}
		if !validReportConfig(input.Type, input.Config) {
			fail(w, 400, "Invalid report configuration")
			return
		}
		var item analytics.SavedReport
		var err error
		if r.Method == http.MethodPatch {
			item, err = s.store.UpdateReport(project, input.ID, input.Name, input.Type, input.Config)
		} else {
			item, err = s.store.SaveReport(project, input.Name, input.Type, actor.Name, input.Config)
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				storeError(w, err)
				return
			}
			fail(w, 400, err.Error())
			return
		}
		if r.Method == http.MethodPatch {
			s.record(actor, "report.updated", project, item.ID, nil)
			respond(w, http.StatusOK, item)
		} else {
			s.record(actor, "report.created", project, item.ID, nil)
			respond(w, http.StatusCreated, item)
		}
	case http.MethodDelete:
		if !requireRole(w, r, "editor") {
			return
		}
		id := r.URL.Query().Get("id")
		if err := s.store.DeleteReport(project, id); err != nil {
			storeError(w, err)
			return
		}
		s.record(actor, "report.deleted", project, id, nil)
		respond(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 405, "Method not allowed")
	}
}

func (s *server) dashboardsAPI(w http.ResponseWriter, r *http.Request, actor principal, project string) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.Dashboards(project)
		if err != nil {
			databaseError(w, err)
			return
		}
		respond(w, 200, items)
	case http.MethodPost:
		if !requireRole(w, r, "editor") {
			return
		}
		var input struct {
			Name      string   `json:"name"`
			ReportIDs []string `json:"report_ids"`
		}
		if decode(w, r, &input) != nil {
			fail(w, 400, "Invalid dashboard")
			return
		}
		item, err := s.store.SaveDashboard(project, input.Name, actor.Name, input.ReportIDs)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		s.record(actor, "dashboard.created", project, item.ID, nil)
		respond(w, 201, item)
	case http.MethodDelete:
		if !requireRole(w, r, "editor") {
			return
		}
		id := r.URL.Query().Get("id")
		if err := s.store.DeleteDashboard(project, id); err != nil {
			storeError(w, err)
			return
		}
		s.record(actor, "dashboard.deleted", project, id, nil)
		respond(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 405, "Method not allowed")
	}
}

func (s *server) annotationsAPI(w http.ResponseWriter, r *http.Request, actor principal, project string) {
	switch r.Method {
	case http.MethodGet:
		from, to, err := dateRange(r)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		items, err := s.store.Annotations(project, from, to)
		if err != nil {
			databaseError(w, err)
			return
		}
		respond(w, 200, items)
	case http.MethodPost:
		if !requireRole(w, r, "editor") {
			return
		}
		var input struct {
			Title       string    `json:"title"`
			Description string    `json:"description"`
			OccurredAt  time.Time `json:"occurred_at"`
		}
		if decode(w, r, &input) != nil {
			fail(w, 400, "Invalid annotation")
			return
		}
		item, err := s.store.AddAnnotation(project, input.Title, input.Description, actor.Name, input.OccurredAt)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		s.record(actor, "annotation.created", project, item.ID, nil)
		respond(w, 201, item)
	case http.MethodDelete:
		if !requireRole(w, r, "editor") {
			return
		}
		id := r.URL.Query().Get("id")
		if err := s.store.DeleteAnnotation(project, id); err != nil {
			storeError(w, err)
			return
		}
		s.record(actor, "annotation.deleted", project, id, nil)
		respond(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 405, "Method not allowed")
	}
}

func (s *server) eventDefinitionsAPI(w http.ResponseWriter, r *http.Request, actor principal, project string) {
	if r.Method == http.MethodGet {
		items, err := s.store.EventDefinitions(project)
		if err != nil {
			databaseError(w, err)
			return
		}
		respond(w, 200, items)
		return
	}
	if r.Method != http.MethodPatch {
		fail(w, 405, "Method not allowed")
		return
	}
	if !requireRole(w, r, "editor") {
		return
	}
	var input analytics.EventDefinitionRecord
	if decode(w, r, &input) != nil {
		fail(w, 400, "Invalid event definition")
		return
	}
	input.ProjectID = project
	item, err := s.store.UpsertEventDefinition(input)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.record(actor, "event_definition.updated", project, item.Event, nil)
	respond(w, 200, item)
}

func (s *server) alertsAPI(w http.ResponseWriter, r *http.Request, actor principal, project string) {
	if r.Method == http.MethodGet {
		items, err := s.store.Alerts(project)
		if err != nil {
			databaseError(w, err)
			return
		}
		respond(w, 200, items)
		return
	}
	if !requireRole(w, r, "editor") {
		return
	}
	if r.Method == http.MethodDelete {
		id := r.URL.Query().Get("id")
		if err := s.store.DeleteAlert(project, id); err != nil {
			storeError(w, err)
			return
		}
		s.record(actor, "alert.deleted", project, id, nil)
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "Method not allowed")
		return
	}
	var input analytics.Alert
	if decode(w, r, &input) != nil {
		fail(w, 400, "Invalid alert")
		return
	}
	input.ProjectID = project
	if input.Metric != "users" && input.Metric != "events" && input.Metric != "sessions" && input.Metric != "pageviews" && input.Metric != "conversion" {
		fail(w, 400, "Invalid metric")
		return
	}
	if input.Comparison != "above" && input.Comparison != "below" && input.Comparison != "changes_by" {
		fail(w, 400, "Invalid comparison")
		return
	}
	item, err := s.store.SaveAlert(input)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.record(actor, "alert.saved", project, item.ID, nil)
	respond(w, 201, item)
}

func (s *server) projectPolicyAPI(w http.ResponseWriter, r *http.Request, actor principal, project string) {
	if !requireRole(w, r, "owner") {
		return
	}
	if r.Method == http.MethodPatch {
		var input struct {
			RetentionDays int `json:"retention_days"`
		}
		if decode(w, r, &input) != nil {
			fail(w, 400, "Invalid retention policy")
			return
		}
		item, err := s.store.UpdateRetention(project, input.RetentionDays)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		s.record(actor, "project.retention_updated", project, project, map[string]any{"days": input.RetentionDays})
		respond(w, 200, item)
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.store.DeleteProject(project); err != nil {
			storeError(w, err)
			return
		}
		s.record(actor, "project.deleted", "", project, nil)
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "Method not allowed")
		return
	}
	var input struct {
		Action     string `json:"action"`
		DistinctID string `json:"distinct_id"`
	}
	if decode(w, r, &input) != nil {
		fail(w, 400, "Invalid action")
		return
	}
	switch strings.TrimSpace(input.Action) {
	case "rotate_key":
		item, err := s.store.RotateProjectKey(project)
		if err != nil {
			storeError(w, err)
			return
		}
		s.record(actor, "project.key_rotated", project, project, nil)
		respond(w, 200, item)
	case "purge_expired":
		n, err := s.store.PurgeExpired(project)
		if err != nil {
			databaseError(w, err)
			return
		}
		s.record(actor, "project.retention_purged", project, project, map[string]any{"events": n})
		respond(w, 200, map[string]int64{"deleted": n})
	case "delete_person":
		if strings.TrimSpace(input.DistinctID) == "" {
			fail(w, 400, "distinct_id is required")
			return
		}
		n, err := s.store.DeletePerson(project, input.DistinctID)
		if err != nil {
			databaseError(w, err)
			return
		}
		s.record(actor, "person.deleted", project, input.DistinctID, map[string]any{"events": n})
		respond(w, 200, map[string]int64{"deleted": n})
	default:
		fail(w, 400, "Unknown project action")
	}
}
