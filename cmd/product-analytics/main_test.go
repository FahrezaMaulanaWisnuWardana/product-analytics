package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"product-analytics/internal/analytics"
	"product-analytics/internal/testdb"
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
		Metrics analytics.Metrics `json:"metrics"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Metrics.Events != 1 {
		t.Fatal("project isolation failed", w.Body.String())
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
