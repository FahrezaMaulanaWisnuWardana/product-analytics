package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"pulse/internal/analytics"
	"pulse/web"
)

type server struct {
	store  *analytics.Store
	token  string
	secure bool
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	importFile := flag.String("import-jsonl", "", "import a legacy events.jsonl file and exit")
	migrateOnly := flag.Bool("migrate-only", false, "apply database migrations and exit")
	demo := flag.Bool("demo", false, "seed a demo project when storage is empty")
	flag.Parse()
	store, err := analytics.Open(os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	maxEvents, err := positiveEnvInt("PULSE_MAX_EVENTS_PER_PROJECT", "PRODUCT_ANALYTICS_MAX_EVENTS_PER_PROJECT", analytics.DefaultMaxEventsPerProject)
	if err != nil {
		log.Fatal(err)
	}
	maxQueryEvents, err := positiveEnvInt("PULSE_MAX_QUERY_EVENTS", "PRODUCT_ANALYTICS_MAX_QUERY_EVENTS", analytics.DefaultMaxQueryEvents)
	if err != nil {
		log.Fatal(err)
	}
	if err = store.SetLimits(maxEvents, maxQueryEvents); err != nil {
		log.Fatal(err)
	}
	if *importFile != "" {
		f, err := os.Open(*importFile)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		result, err := store.ImportJSONL(f)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Imported %d projects and %d events; source file preserved", result.Projects, result.Events)
		return
	}
	if *migrateOnly {
		log.Print("Database migrations are up to date")
		return
	}
	token := envValue("PULSE_ADMIN_TOKEN", "PRODUCT_ANALYTICS_ADMIN_TOKEN")
	if token == "" {
		log.Fatal("PULSE_ADMIN_TOKEN is required; generate a long random value and keep it out of logs")
	}
	secure, err := boolEnv("PULSE_SECURE_COOKIES", "PRODUCT_ANALYTICS_SECURE_COOKIES", true)
	if err != nil {
		log.Fatal(err)
	}
	hasProjects, err := store.HasProjects()
	if err != nil {
		log.Fatal(err)
	}
	if *demo && !hasProjects {
		if err = seed(store); err != nil {
			log.Fatal(err)
		}
	}
	s := &server{store: store, token: token, secure: secure}
	httpServer := &http.Server{Addr: *addr, Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	log.Printf("Pulse is running at http://%s", *addr)
	if err = httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "pulse_admin", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure})
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/capture", s.capture)
	mux.HandleFunc("GET /api/installation", func(w http.ResponseWriter, r *http.Request) {
		hasProjects, err := s.store.HasProjects()
		if err != nil {
			databaseError(w, err)
			return
		}
		respond(w, http.StatusOK, map[string]bool{"setup_required": !hasProjects})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.store.Ping(ctx); err != nil {
			databaseError(w, err)
			return
		}
		respond(w, 200, map[string]string{"status": "ready", "storage": "postgresql"})
	})
	mux.Handle("/api/", s.auth(http.HandlerFunc(s.api)))
	mux.Handle("/", http.FileServer(http.FS(web.Files)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}
func databaseError(w http.ResponseWriter, err error) {
	if errors.Is(err, analytics.ErrEventQuota) {
		fail(w, http.StatusTooManyRequests, "Project event quota reached. Increase the configured quota only with matching storage and query capacity.")
		return
	}
	if errors.Is(err, analytics.ErrQueryEventLimit) {
		fail(w, http.StatusRequestEntityTooLarge, "This query matches too many events. Narrow the date range or increase the configured query limit with matching capacity.")
		return
	}
	log.Printf("Database operation failed: %v", err)
	fail(w, http.StatusServiceUnavailable, "Database unavailable. Please try again.")
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}
func (s *server) valid(token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1
}
func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.secure && !loopbackRequest(r) {
			fail(w, http.StatusForbidden, "Insecure administrator access requires a loopback connection and host. Enable secure cookies behind HTTPS.")
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if c, err := r.Cookie("pulse_admin"); err == nil && token == "" {
			token = c.Value
		}
		if c, err := r.Cookie("product_analytics_admin"); err == nil && token == "" {
			token = c.Value
		}
		if !s.valid(token) {
			fail(w, 401, "Sign in with your admin token to continue.")
			return
		}
		if r.Method != "GET" && r.Header.Get("Origin") != "" && !sameOrigin(r) {
			fail(w, 403, "Cross-origin admin requests are not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func sameOrigin(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && u.Host == r.Host
}
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if !s.secure && !loopbackRequest(r) {
		fail(w, http.StatusForbidden, "Insecure administrator login requires a loopback connection and host. Enable secure cookies behind HTTPS.")
		return
	}
	if r.Header.Get("Origin") != "" && !sameOrigin(r) {
		fail(w, 403, "Invalid origin")
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if decode(w, r, &input) != nil {
		fail(w, 400, "Invalid JSON")
		return
	}
	if !s.valid(input.Token) {
		fail(w, 401, "That admin token is not valid.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "pulse_admin", Value: input.Token, Path: "/", MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure})
	respond(w, 200, map[string]bool{"ok": true})
}
func loopbackHost(hostport string) bool {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func loopbackRequest(r *http.Request) bool {
	return loopbackHost(r.Host) && loopbackHost(r.RemoteAddr)
}
func envValue(name, legacyName string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return os.Getenv(legacyName)
}
func positiveEnvInt(name, legacyName string, fallback int) (int, error) {
	raw := strings.TrimSpace(envValue(name, legacyName))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}
func boolEnv(name, legacyName string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(envValue(name, legacyName))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return value, nil
}
func (s *server) capture(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(204)
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "Use POST")
		return
	}
	var input struct {
		APIKey string            `json:"api_key"`
		Events []analytics.Event `json:"events"`
	}
	if err := decode(w, r, &input); err != nil {
		fail(w, 400, "Invalid JSON or body larger than 1 MB")
		return
	}
	p, ok, err := s.store.ByKey(input.APIKey)
	if err != nil {
		databaseError(w, err)
		return
	}
	if !ok {
		fail(w, 401, "Unknown project key")
		return
	}
	origin := r.Header.Get("Origin")
	allowed := origin == ""
	for _, o := range p.Origins {
		if origin == o {
			allowed = true
		}
	}
	if !allowed {
		fail(w, 403, "This origin is not allowed for this project")
		return
	}
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	if len(input.Events) == 0 || len(input.Events) > 100 {
		fail(w, 400, "Send between 1 and 100 events")
		return
	}
	now := time.Now().UTC()
	for i := range input.Events {
		e := &input.Events[i]
		e.Name = strings.TrimSpace(e.Name)
		e.DistinctID = strings.TrimSpace(e.DistinctID)
		if e.Name == "" || len(e.Name) > 200 || e.DistinctID == "" || len(e.DistinctID) > 200 || len(e.SessionID) > 200 || len(e.ID) > 200 {
			fail(w, 400, "Each event needs an event name and distinct_id of 1–200 characters")
			return
		}
		if e.Timestamp.IsZero() {
			e.Timestamp = now
		}
		if e.Timestamp.After(now.Add(5 * time.Minute)) {
			fail(w, 400, "Event timestamp cannot be more than 5 minutes in the future")
			return
		}
		if e.ID == "" {
			e.ID = analytics.ID("evt_")
		} else {
			e.ID = p.ID + ":" + e.ID
		}
		e.ProjectID = p.ID
		if e.Properties == nil {
			e.Properties = map[string]any{}
		}
	}
	accepted, err := s.store.AddBatch(input.Events)
	if err != nil {
		databaseError(w, err)
		return
	}
	respond(w, 202, map[string]int{"accepted": accepted})
}
func (s *server) api(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/projects" {
		switch r.Method {
		case "GET":
			projects, err := s.store.Projects()
			if err != nil {
				databaseError(w, err)
				return
			}
			respond(w, 200, projects)
		case "POST", "PATCH":
			var v struct {
				ID      string   `json:"id"`
				Name    string   `json:"name"`
				Origins []string `json:"origins"`
			}
			if decode(w, r, &v) != nil {
				fail(w, 400, "Invalid project")
				return
			}
			if name := strings.TrimSpace(v.Name); name == "" || len(name) > 80 {
				fail(w, 400, "project name must contain 1–80 characters")
				return
			}
			for _, o := range v.Origins {
				u, err := url.Parse(o)
				if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
					fail(w, 400, "Use exact origins such as https://example.com, without a path or trailing slash")
					return
				}
			}
			var p analytics.Project
			var err error
			status := http.StatusCreated
			if r.Method == "PATCH" {
				p, err = s.store.UpdateProject(v.ID, v.Name, v.Origins)
				status = http.StatusOK
			} else {
				p, err = s.store.CreateProject(v.Name, v.Origins)
			}
			if err != nil {
				if errors.Is(err, analytics.ErrProjectNotFound) {
					fail(w, 404, "Project not found")
				} else {
					databaseError(w, err)
				}
				return
			}
			respond(w, status, p)
		default:
			fail(w, 405, "Method not allowed")
		}
		return
	}
	if r.Method != "GET" {
		fail(w, 405, "Method not allowed")
		return
	}
	p, ok, err := s.store.Project(r.URL.Query().Get("project"))
	if err != nil {
		databaseError(w, err)
		return
	}
	if !ok {
		fail(w, 404, "Project not found")
		return
	}
	from, to, err := dateRange(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	events, err := s.store.Events(p.ID, from, to)
	if err != nil {
		databaseError(w, err)
		return
	}
	switch r.URL.Path {
	case "/api/overview":
		previous, err := s.store.Events(p.ID, from.Add(-to.Sub(from)), from)
		if err != nil {
			databaseError(w, err)
			return
		}
		recent := append([]analytics.Event{}, events...)
		sort.Slice(recent, func(i, j int) bool { return recent[i].Timestamp.After(recent[j].Timestamp) })
		if len(recent) > 8 {
			recent = recent[:8]
		}
		respond(w, 200, map[string]any{"metrics": analytics.Measure(events), "previous": analytics.Measure(previous), "series": analytics.Series(events, from, to), "top_events": analytics.Breakdown(events, ""), "pages": analytics.Breakdown(filterEvents(events, "$pageview"), "$pathname"), "browsers": analytics.Breakdown(events, "$browser"), "recent": recent, "from": from, "to": to})
	case "/api/events":
		name := r.URL.Query().Get("event")
		person := r.URL.Query().Get("person")
		search := strings.ToLower(r.URL.Query().Get("q"))
		out := []analytics.Event{}
		for _, e := range events {
			if name != "" && e.Name != name {
				continue
			}
			if person != "" && e.DistinctID != person {
				continue
			}
			if search != "" && !strings.Contains(strings.ToLower(e.Name+" "+e.DistinctID), search) {
				continue
			}
			out = append(out, e)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
		total := len(out)
		if len(out) > 500 {
			out = out[:500]
		}
		respond(w, 200, map[string]any{"events": out, "total": total, "names": analytics.Breakdown(events, "")})
	case "/api/people":
		respond(w, 200, analytics.People(events))
	case "/api/funnel":
		steps := r.URL.Query()["step"]
		if len(steps) < 2 || len(steps) > 8 {
			fail(w, 400, "Choose 2–8 funnel steps")
			return
		}
		respond(w, 200, analytics.Funnel(events, steps, 7*24*time.Hour))
	case "/api/retention":
		all, err := s.store.Events(p.ID, time.Time{}, to)
		if err != nil {
			databaseError(w, err)
			return
		}
		respond(w, 200, analytics.Retention(all, from, to))
	case "/api/export":
		w.Header().Set("Content-Disposition", "attachment; filename=pulse-events.json")
		respond(w, 200, events)
	default:
		fail(w, 404, "Endpoint not found")
	}
}
func filterEvents(events []analytics.Event, name string) []analytics.Event {
	out := []analytics.Event{}
	for _, e := range events {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}
func dateRange(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	to := now
	from := now.Truncate(24*time.Hour).AddDate(0, 0, -29)
	if v := r.URL.Query().Get("from"); v != "" {
		var err error
		from, err = time.Parse("2006-01-02", v)
		if err != nil {
			return from, to, fmt.Errorf("invalid start date")
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		d, err := time.Parse("2006-01-02", v)
		if err != nil {
			return from, to, fmt.Errorf("invalid end date")
		}
		to = d.AddDate(0, 0, 1)
		if to.After(now) {
			to = now
		}
	}
	if !from.Before(to) || to.Sub(from) > 366*24*time.Hour {
		return from, to, errors.New("Choose a date range between 1 and 366 days")
	}
	return from, to, nil
}
func seed(store *analytics.Store) error {
	p, err := store.CreateProject("Acme Web", []string{"http://localhost:3000", "http://127.0.0.1:3000"})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	start := now.Truncate(24*time.Hour).AddDate(0, 0, -59)
	batch := []analytics.Event{}
	for day := 0; day < 60; day++ {
		date := start.AddDate(0, 0, day)
		count := 18 + day/2 + (day*7)%13
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("user_%03d", (i*3+day*5)%210)
			if i%3 == 0 {
				id = fmt.Sprintf("user_%03d", 210+day*6+i%18)
			}
			session := fmt.Sprintf("ses_%d_%d", day, i)
			t := date.Add(time.Duration((i*37+day*11)%(23*60)) * time.Minute)
			if t.After(now) {
				continue
			}
			props := map[string]any{"$pathname": []string{"/", "/dashboard", "/pricing", "/integrations", "/docs"}[(i+day)%5], "$browser": []string{"Chrome", "Chrome", "Safari", "Chrome", "Firefox", "Safari", "Edge"}[i%7], "$device": []string{"Desktop", "Desktop", "Mobile"}[i%3], "$country": []string{"United States", "Indonesia", "Germany", "United Kingdom"}[i%4], "plan": []string{"Free", "Pro", "Team"}[i%3]}
			names := []string{"$pageview"}
			if i%6 != 0 {
				names = append(names, "signed_up")
			}
			if i%3 != 0 {
				names = append(names, "project_created")
			}
			if i%5 < 2 {
				names = append(names, "subscription_started")
			}
			if i%7 == 0 {
				names = append(names, "$pageview", "feature_used")
			}
			for n, name := range names {
				et := t.Add(time.Duration(n) * time.Minute)
				if et.After(now) {
					continue
				}
				batch = append(batch, analytics.Event{ID: analytics.ID("demo_"), ProjectID: p.ID, Name: name, DistinctID: id, SessionID: session, Timestamp: et, Properties: props})
			}
		}
	}
	_, err = store.AddBatch(batch)
	return err
}
