package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pulse/internal/analytics"
	"pulse/internal/testdb"
)

func testServer(t *testing.T) (*server, http.Handler) {
	t.Helper()
	s, err := analytics.Open(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	srv := &server{store: s, token: "test-admin", secure: true}
	return srv, srv.routes()
}
func request(h http.Handler, method, path, body, origin, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestAdminAuthAndLogin(t *testing.T) {
	_, h := testServer(t)
	if w := request(h, "GET", "/api/projects", "", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(h, "POST", "/api/login", `{"token":"wrong"}`, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(h, "POST", "/api/login", `{"token":"test-admin"}`, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe auth cookie")
	}
	r := httptest.NewRequest("GET", "/api/projects", nil)
	r.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if w := request(h, "POST", "/api/projects", `{"name":"x"}`, "https://evil.example", "test-admin"); w.Code != 403 {
		t.Fatal("cross-origin admin write accepted", w.Code)
	}
}

func TestSavedTypedFunnelAPI(t *testing.T) {
	s, h := testServer(t)
	p, err := s.store.CreateProject("Funnels", nil)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/reports?project=" + p.ID
	body := `{"name":"Pricing","type":"funnels","config":{"view":"funnels","version":2,"window_minutes":1440,"steps":[{"type":"page","operator":"eq","value":"/"},{"type":"event","operator":"eq","value":"signup"}]}}`
	w := request(h, "POST", path, body, "", "test-admin")
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var report analytics.SavedReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(body, `"name":"Pricing"`, `"id":"`+report.ID+`","name":"Activation"`, 1)
	w = request(h, "PATCH", path, updated, "", "test-admin")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Activation") {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	w = request(h, "POST", "/api/funnels/query?project="+p.ID, `{"window_minutes":1440,"steps":[{"type":"page","operator":"eq","value":"/"},{"type":"event","operator":"starts_with","value":"signup"}]}`, "", "test-admin")
	if w.Code != 400 {
		t.Fatalf("invalid operator accepted: %d", w.Code)
	}
}
func TestInsecureAdminAccessIsLoopbackOnly(t *testing.T) {
	s, _ := testServer(t)
	s.secure = false
	h := s.routes()
	if w := request(h, "POST", "/api/login", `{"token":"test-admin"}`, "", ""); w.Code != http.StatusForbidden {
		t.Fatal("remote-style insecure login accepted", w.Code)
	}
	spoofed := httptest.NewRequest("POST", "http://localhost/api/login", strings.NewReader(`{"token":"test-admin"}`))
	spoofed.RemoteAddr = "203.0.113.10:4567"
	spoofedResponse := httptest.NewRecorder()
	h.ServeHTTP(spoofedResponse, spoofed)
	if spoofedResponse.Code != http.StatusForbidden {
		t.Fatal("spoofed loopback Host accepted from remote peer", spoofedResponse.Code)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/login", strings.NewReader(`{"token":"test-admin"}`))
	r.RemoteAddr = "127.0.0.1:4567"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal("loopback insecure login rejected", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Secure {
		t.Fatal("loopback development cookie should be explicitly non-Secure")
	}
}
func TestCaptureValidationCORSIsolationAndDedup(t *testing.T) {
	s, h := testServer(t)
	p, _ := s.store.CreateProject("A", []string{"https://app.example"})
	other, _ := s.store.CreateProject("B", nil)
	body := fmt.Sprintf(`{"api_key":%q,"events":[{"id":"once","event":"signed_up","distinct_id":"u1"}]}`, p.Key)
	if w := request(h, "POST", "/capture", body, "https://evil.example", ""); w.Code != 403 {
		t.Fatal("bad origin accepted", w.Code)
	}
	if w := request(h, "POST", "/capture", body, "https://app.example", ""); w.Code != 202 || w.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatal(w.Code, w.Body.String())
	}
	w := request(h, "POST", "/capture", body, "", "")
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"accepted":0`) {
		t.Fatal("duplicate accepted", w.Body.String())
	}
	otherBody := fmt.Sprintf(`{"api_key":%q,"events":[{"id":"once","event":"signed_up","distinct_id":"u1"}]}`, other.Key)
	if w := request(h, "POST", "/capture", otherBody, "", ""); w.Code != 202 || !strings.Contains(w.Body.String(), `"accepted":1`) {
		t.Fatal("dedup should be project-scoped", w.Body.String())
	}
	bad := fmt.Sprintf(`{"api_key":%q,"events":[{"event":"good","distinct_id":"u"},{"event":"bad"}]}`, p.Key)
	if w := request(h, "POST", "/capture", bad, "", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if len(testEvents(t, s.store, p.ID)) != 1 {
		t.Fatal("invalid batch was partially accepted")
	}
	future := fmt.Sprintf(`{"api_key":%q,"events":[{"event":"good","distinct_id":"u","timestamp":%q}]}`, p.Key, time.Now().Add(time.Hour).Format(time.RFC3339))
	if w := request(h, "POST", "/capture", future, "", ""); w.Code != 400 {
		t.Fatal("future accepted", w.Code)
	}
	w = request(h, "GET", "/api/overview?project="+other.ID, "", "", "test-admin")
	var response struct {
		Metrics  analytics.Metrics         `json:"metrics"`
		Decision analytics.DecisionContext `json:"decision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Metrics.Events != 1 {
		t.Fatal("project isolation failed", w.Body.String())
	}
	if response.Decision.Readiness != "collecting" || response.Decision.ActiveDays != 1 || response.Decision.TopEvent.Name != "signed_up" {
		t.Fatal("decision context missing from overview", w.Body.String())
	}
	if w := request(h, "POST", "/capture", `{"api_key":"wrong","events":[]}`, "", ""); w.Code != 401 {
		t.Fatal("invalid ingestion key accepted")
	}
}
func TestProjectSettingsAndInvalidDates(t *testing.T) {
	s, h := testServer(t)
	p, _ := s.store.CreateProject("Before", nil)
	body := fmt.Sprintf(`{"id":%q,"name":"After","origins":["https://app.example"]}`, p.ID)
	if w := request(h, "PATCH", "/api/projects", body, "", "test-admin"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	updated, _, err := s.store.Project(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "After" {
		t.Fatal("name not updated")
	}
	if w := request(h, "POST", "/api/projects", `{"name":"bad","origins":["https://app.example/"]}`, "", "test-admin"); w.Code != 400 {
		t.Fatal("path accepted as origin")
	}
	if w := request(h, "GET", "/api/events?project="+p.ID+"&from=oops", "", "", "test-admin"); w.Code != 400 {
		t.Fatal("invalid date accepted")
	}
	if w := request(h, "GET", "/api/funnel?project="+p.ID+"&step=a", "", "", "test-admin"); w.Code != 400 {
		t.Fatal("single step accepted")
	}
}

func TestWorkspaceRolesAndSharedReports(t *testing.T) {
	s, h := testServer(t)
	p, err := s.store.CreateProject("Shared", nil)
	if err != nil {
		t.Fatal(err)
	}
	created := request(h, http.MethodPost, "/api/access-tokens", `{"name":"Analyst","role":"viewer"}`, "", "test-admin")
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body.String())
	}
	var tokenResult struct {
		Token  string                `json:"token"`
		Access analytics.AccessToken `json:"access"`
	}
	if err = json.Unmarshal(created.Body.Bytes(), &tokenResult); err != nil {
		t.Fatal(err)
	}
	if tokenResult.Token == "" || tokenResult.Access.Role != "viewer" {
		t.Fatal("token response omitted one-time credential or role")
	}
	if w := request(h, http.MethodGet, "/api/me", "", "", tokenResult.Token); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"role":"viewer"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	path := "/api/reports?project=" + p.ID
	if w := request(h, http.MethodPost, path, `{"name":"Blocked","type":"overview","config":{}}`, "", tokenResult.Token); w.Code != http.StatusForbidden {
		t.Fatal("viewer write accepted", w.Code, w.Body.String())
	}
	createdReport := request(h, http.MethodPost, path, `{"name":"Activation","type":"funnels","config":{"steps":["signed_up","activated"]}}`, "", "test-admin")
	if createdReport.Code != http.StatusCreated {
		t.Fatal(createdReport.Code, createdReport.Body.String())
	}
	maliciousReport := request(h, http.MethodPost, path, `{"name":"Injected","type":"overview","config":{"metric":"users\"><form action=https://example.test>","view":"overview"}}`, "", "test-admin")
	if maliciousReport.Code != http.StatusBadRequest {
		t.Fatal("unsafe saved-report metric accepted", maliciousReport.Code, maliciousReport.Body.String())
	}
	if w := request(h, http.MethodGet, path, "", "", tokenResult.Token); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Activation") {
		t.Fatal("shared report unavailable to viewer", w.Code, w.Body.String())
	}
	if w := request(h, http.MethodGet, "/api/access-tokens", "", "", tokenResult.Token); w.Code != http.StatusForbidden {
		t.Fatal("viewer listed workspace credentials", w.Code)
	}
	if w := request(h, http.MethodDelete, "/api/access-tokens?id="+tokenResult.Access.ID, "", "", "test-admin"); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, http.MethodGet, "/api/me", "", "", tokenResult.Token); w.Code != http.StatusUnauthorized {
		t.Fatal("revoked token remained usable", w.Code)
	}
}

func TestEventPagination(t *testing.T) {
	s, h := testServer(t)
	p, err := s.store.CreateProject("Paginated", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-25 * time.Second)
	events := make([]analytics.Event, 25)
	for i := range events {
		events[i] = analytics.Event{ID: fmt.Sprintf("event-%02d", i), ProjectID: p.ID, Name: "viewed", DistinctID: "person", Timestamp: now.Add(time.Duration(i) * time.Second)}
	}
	if _, err = s.store.AddBatch(events); err != nil {
		t.Fatal(err)
	}
	w := request(h, http.MethodGet, "/api/events?project="+p.ID+"&page=2&page_size=10", "", "", "test-admin")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Events   []analytics.Event `json:"events"`
		Total    int               `json:"total"`
		Page     int               `json:"page"`
		PageSize int               `json:"page_size"`
		Pages    int               `json:"pages"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 10 || result.Total != 25 || result.Page != 2 || result.PageSize != 10 || result.Pages != 3 {
		t.Fatalf("unexpected pagination: %+v", result)
	}
	if result.Events[0].ID != "event-14" || result.Events[9].ID != "event-05" {
		t.Fatal("events are not paginated in newest-first order")
	}
	if w = request(h, http.MethodGet, "/api/events?project="+p.ID+"&page_size=500", "", "", "test-admin"); w.Code != http.StatusBadRequest {
		t.Fatal("oversized page accepted", w.Code)
	}
}

func TestProjectDeletionCascadesExistingData(t *testing.T) {
	s, h := testServer(t)
	p, err := s.store.CreateProject("Disposable", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.Add(analytics.Event{
		ID:         "existing-event",
		ProjectID:  p.ID,
		Name:       "signed_up",
		DistinctID: "person-1",
		Timestamp:  time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	w := request(h, http.MethodDelete, "/api/project-policy?project="+p.ID, "", "", "test-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("non-empty project deletion failed: %d %s", w.Code, w.Body.String())
	}
	if _, ok, err := s.store.Project(p.ID); err != nil || ok {
		t.Fatalf("project still exists after deletion: ok=%v err=%v", ok, err)
	}
}

func TestFirstInstallStatus(t *testing.T) {
	s, h := testServer(t)
	status := func(want bool) {
		t.Helper()
		w := request(h, "GET", "/api/installation", "", "", "")
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
		var result map[string]bool
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result) != 1 || result["setup_required"] != want {
			t.Fatalf("unexpected installation status: %s", w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("installation state must not be cached")
		}
	}
	status(true)
	// Setup cannot create a project or read keys without the admin token.
	if w := request(h, "POST", "/api/projects", `{"name":"First product"}`, "", ""); w.Code != 401 {
		t.Fatal("unauthenticated project creation allowed")
	}
	if w := request(h, "POST", "/api/projects", `{"name":"First product"}`, "", "test-admin"); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	status(false)
	// Installation completion comes from persisted projects, not browser storage.
	dir := testdb.URL(t)
	store, err := analytics.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateProject("Existing", nil); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := analytics.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredServer := &server{store: restored, token: s.token}
	w := request(restoredServer.routes(), "GET", "/api/installation", "", "", "")
	if !strings.Contains(w.Body.String(), `"setup_required":false`) {
		t.Fatal("setup reappeared after restart", w.Body.String())
	}
}

func TestIndexServesVersionedAssets(t *testing.T) {
	s, _ := testServer(t)
	w := request(s.routes(), http.MethodGet, "/", "", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("index served with status %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index must always revalidate, got Cache-Control %q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, `src="/app.js?v=`) {
		t.Fatalf("index must reference a fingerprinted app.js: %s", body)
	}
	if strings.Contains(body, `src="/app.js"`) {
		t.Fatalf("index still references an unfingerprinted app.js: %s", body)
	}
	if strings.Contains(body, `href="/style.css"`) {
		t.Fatalf("index must reference a fingerprinted style.css: %s", body)
	}
}

func TestFingerprintedAssetFetch(t *testing.T) {
	s, _ := testServer(t)
	w := request(s.routes(), http.MethodGet, "/app.js?v=anything", "", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("fingerprinted asset fetch failed: %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("fingerprinted asset must be immutable, got %q", got)
	}
	// Plain requests without a fingerprint still work for anything that links
	// to app.js directly.
	if w := request(s.routes(), http.MethodGet, "/app.js", "", "", ""); w.Code != http.StatusOK {
		t.Fatalf("plain asset fetch failed: %d", w.Code)
	}
}

func testEvents(t *testing.T, s *analytics.Store, project string) []analytics.Event {
	t.Helper()
	events, err := s.Events(project, time.Time{}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestDatabaseOutageDoesNotMasqueradeAsFirstInstall(t *testing.T) {
	s, h := testServer(t)
	p, err := s.store.CreateProject("Before outage", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/installation", "/api/projects", "/readyz", "/api/overview?project=" + p.ID} {
		w := request(h, "GET", path, "", "", "test-admin")
		if w.Code != 503 {
			t.Fatalf("%s returned %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "setup_required") {
			t.Fatal("outage incorrectly triggered first-install setup")
		}
	}
	w := request(h, "POST", "/api/projects", `{"name":"During outage"}`, "", "test-admin")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}
