package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // embed zone data for the minimal container image

	"pulse/internal/analytics"
	"pulse/web"
)

type server struct {
	store          *analytics.Store
	token          string
	secure         bool
	country        *countryLookup
	loginLimiter   *rateLimiter
	captureLimiter *rateLimiter
	metrics        *metricsState
}

type principal struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type principalContextKey struct{}

type rateBucket struct {
	window int64
	count  int
}

// rateLimiter is a small fixed-window limiter for the two public endpoints.
// It is a local safety net; operators should still rate-limit at the proxy.
type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	buckets map[string]rateBucket
}

func newRateLimiter(limit int) *rateLimiter {
	if limit <= 0 {
		return nil
	}
	return &rateLimiter{limit: limit, buckets: map[string]rateBucket{}}
}

func (l *rateLimiter) allow(key string) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	window := time.Now().Unix() / 60
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b.window != window {
		b = rateBucket{window: window}
	}
	if b.count >= l.limit {
		l.buckets[key] = b
		return false
	}
	b.count++
	l.buckets[key] = b
	if len(l.buckets) > 10000 {
		for k, v := range l.buckets {
			if v.window != window {
				delete(l.buckets, k)
			}
		}
	}
	return true
}

var requestMetrics *metricsState

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

type metricsState struct {
	mu             sync.Mutex
	requests       map[string]int64
	authFailures   int64
	eventsAccepted int64
	databaseErrors int64
}

func (m *metricsState) observe(method string, status int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[method+" "+strconv.Itoa(status)]++
}
func (m *metricsState) authFailure() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authFailures++
}
func (m *metricsState) eventAccepted(n int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventsAccepted += n
}
func (m *metricsState) databaseError() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.databaseErrors++
}
func (m *metricsState) write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if m == nil {
		_, _ = io.WriteString(w, "")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	b.WriteString("# HELP pulse_requests_total HTTP requests by method and status.\n# TYPE pulse_requests_total counter\n")
	keys := make([]string, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := strings.SplitN(k, " ", 2)
		b.WriteString("pulse_requests_total{method=\"" + parts[0] + "\",status=\"" + parts[1] + "\"} " + strconv.FormatInt(m.requests[k], 10) + "\n")
	}
	b.WriteString("# HELP pulse_auth_failures_total Rejected authentication attempts.\n# TYPE pulse_auth_failures_total counter\n")
	b.WriteString("pulse_auth_failures_total " + strconv.FormatInt(m.authFailures, 10) + "\n")
	b.WriteString("# HELP pulse_events_accepted_total Events accepted by /capture.\n# TYPE pulse_events_accepted_total counter\n")
	b.WriteString("pulse_events_accepted_total " + strconv.FormatInt(m.eventsAccepted, 10) + "\n")
	b.WriteString("# HELP pulse_database_errors_total Database failures returned to clients.\n# TYPE pulse_database_errors_total counter\n")
	b.WriteString("pulse_database_errors_total " + strconv.FormatInt(m.databaseErrors, 10) + "\n")
	_, _ = io.WriteString(w, b.String())
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
	auditRetentionDays, err := positiveEnvInt("PULSE_AUDIT_RETENTION_DAYS", "PRODUCT_ANALYTICS_AUDIT_RETENTION_DAYS", analytics.DefaultAuditRetentionDays)
	if err != nil {
		log.Fatal(err)
	}
	tokenRetentionDays, err := positiveEnvInt("PULSE_REVOKED_TOKEN_RETENTION_DAYS", "PRODUCT_ANALYTICS_REVOKED_TOKEN_RETENTION_DAYS", analytics.DefaultTokenRetentionDays)
	if err != nil {
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
	country, err := openCountryLookup(strings.TrimSpace(os.Getenv("PULSE_GEOIP_DATABASE")), os.Getenv("PULSE_TRUSTED_PROXIES"))
	if err != nil {
		log.Fatal(err)
	}
	if country != nil {
		defer country.db.Close()
	}
	loginLimit, err := positiveEnvInt("PULSE_LOGIN_LIMIT_PER_MINUTE", "", 10)
	if err != nil {
		log.Fatal(err)
	}
	captureLimit, err := positiveEnvInt("PULSE_CAPTURE_LIMIT_PER_MINUTE", "", 600)
	if err != nil {
		log.Fatal(err)
	}
	requestMetrics = &metricsState{requests: map[string]int64{}}
	s := &server{
		store:          store,
		token:          token,
		secure:         secure,
		country:        country,
		loginLimiter:   newRateLimiter(loginLimit),
		captureLimiter: newRateLimiter(captureLimit),
		metrics:        requestMetrics,
	}
	httpServer := &http.Server{Addr: *addr, Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go retentionWorker(ctx, store, auditRetentionDays, tokenRetentionDays)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	scheme := "http"
	if s.secure {
		scheme = "https"
	}
	log.Printf("Pulse is running at %s://%s (terminate TLS at your proxy when secure cookies are enabled)", scheme, *addr)
	if err = httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func retentionWorker(ctx context.Context, store *analytics.Store, auditRetentionDays, tokenRetentionDays int) {
	run := func() {
		deleted, err := store.PurgeAllExpired()
		if err != nil {
			log.Printf("Retention cleanup failed: %v", err)
			return
		}
		if deleted > 0 {
			log.Printf("Retention cleanup removed %d expired events", deleted)
		}
		now := time.Now().UTC()
		audits, tokens, err := store.PurgeWorkspaceHistory(now.AddDate(0, 0, -auditRetentionDays), now.AddDate(0, 0, -tokenRetentionDays))
		if err != nil {
			log.Printf("Workspace history cleanup failed: %v", err)
			return
		}
		if audits > 0 || tokens > 0 {
			log.Printf("Workspace history cleanup removed %d audit rows and %d revoked tokens", audits, tokens)
		}
		sessions, err := store.PurgeAdminSessions(now)
		if err != nil {
			log.Printf("Session cleanup failed: %v", err)
			return
		}
		if sessions > 0 {
			log.Printf("Session cleanup removed %d expired admin sessions", sessions)
		}
	}
	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if !s.secure && !loopbackRequest(r) {
			fail(w, http.StatusForbidden, "Insecure administrator access requires a loopback connection and host. Enable secure cookies behind HTTPS.")
			return
		}
		if r.Header.Get("Origin") != "" && !s.sameOrigin(r) {
			fail(w, 403, "Invalid origin")
			return
		}
		for _, name := range []string{"pulse_admin", "product_analytics_admin"} {
			if c, err := r.Cookie(name); err == nil && c.Value != "" && !s.valid(c.Value) {
				if revokeErr := s.store.RevokeAdminSession(tokenHash(c.Value)); revokeErr != nil {
					log.Printf("Session revoke failed: %v", revokeErr)
				}
			}
		}
		http.SetCookie(w, &http.Cookie{Name: "pulse_admin", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure})
		http.SetCookie(w, &http.Cookie{Name: "product_analytics_admin", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure})
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
	mux.Handle("GET /metrics", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.metrics.write(w)
	})))
	mux.Handle("/api/", s.auth(http.HandlerFunc(s.api)))
	mux.Handle("/", staticAssets())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; object-src 'none'")
		if s.secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(rec, r)
		s.metrics.observe(r.Method, rec.status)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// staticAssets serves the embedded web files with cache busting. index.html is
// always revalidated and references app.js and style.css with a content hash
// query, so a new build immediately gets new asset URLs even behind caches
// that ignore origin revalidation. Fingerprinted assets are immutable.
func staticAssets() http.Handler {
	files := http.FileServer(http.FS(web.Files))
	appVersion := assetVersion(web.Files, "app.js")
	styleVersion := assetVersion(web.Files, "style.css")
	index, err := fs.ReadFile(web.Files, "index.html")
	if err != nil {
		log.Fatalf("embedded index.html is unreadable: %v", err)
	}
	replacer := strings.NewReplacer(
		`href="/style.css"`, `href="/style.css?v=`+styleVersion+`"`,
		`src="/app.js"`, `src="/app.js?v=`+appVersion+`"`,
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			if r.URL.Path == "/app.js" || strings.HasPrefix(r.URL.Path, "/app.js?") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(replacer.Replace(string(index))))
	})
}

// assetVersion returns a short hex fingerprint of an embedded file's contents.
func assetVersion(fsys fs.FS, name string) string {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "1"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:4])
}

func databaseError(w http.ResponseWriter, err error) {
	requestMetrics.databaseError()
	if errors.Is(err, analytics.ErrEventQuota) {
		fail(w, http.StatusTooManyRequests, "Project event quota reached. Increase the configured quota only with matching storage and query capacity.")
		return
	}
	if errors.Is(err, analytics.ErrQueryEventLimit) {
		fail(w, http.StatusRequestEntityTooLarge, "This query matches too many events. Narrow the date range or increase the configured query limit with matching capacity.")
		return
	}
	if errors.Is(err, analytics.ErrEventBytesLimit) {
		fail(w, http.StatusRequestEntityTooLarge, "Event properties are too large.")
		return
	}
	if errors.Is(err, analytics.ErrQueryBytesLimit) {
		fail(w, http.StatusRequestEntityTooLarge, "This query contains too much event data. Narrow the date range.")
		return
	}
	if errors.Is(err, analytics.ErrCollaborationLimit) {
		fail(w, http.StatusRequestEntityTooLarge, "This collection is too large. Delete unused items before listing more.")
		return
	}
	if errors.Is(err, analytics.ErrWorkspaceLimit) {
		fail(w, http.StatusTooManyRequests, "Workspace capacity reached. Delete unused projects or allow revoked-token cleanup to complete.")
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

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func principalFrom(r *http.Request) principal {
	p, _ := r.Context().Value(principalContextKey{}).(principal)
	return p
}

func roleRank(role string) int {
	switch role {
	case "owner":
		return 3
	case "editor":
		return 2
	case "viewer":
		return 1
	default:
		return 0
	}
}

func requireRole(w http.ResponseWriter, r *http.Request, minimum string) bool {
	if roleRank(principalFrom(r).Role) < roleRank(minimum) {
		fail(w, http.StatusForbidden, "Your role does not allow this action.")
		return false
	}
	return true
}

func (s *server) authenticate(token string) (principal, bool, error) {
	if s.valid(token) {
		return principal{ID: "owner", Name: "Administrator", Role: "owner"}, true, nil
	}
	if token == "" {
		return principal{}, false, nil
	}
	session, err := s.store.AuthenticateAdminSession(tokenHash(token))
	if err != nil {
		return principal{}, false, err
	}
	if session {
		return principal{ID: "session", Name: "Administrator", Role: "owner"}, true, nil
	}
	member, ok, err := s.store.AuthenticateAccessToken(tokenHash(token))
	if err != nil || !ok {
		return principal{}, ok, err
	}
	return principal{ID: member.ID, Name: member.Name, Role: member.Role}, true, nil
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
		actor, ok, err := s.authenticate(token)
		if err != nil {
			databaseError(w, err)
			return
		}
		if !ok {
			s.metrics.authFailure()
			fail(w, 401, "Sign in with your admin token to continue.")
			return
		}
		if r.Method != "GET" && r.Header.Get("Origin") != "" && !s.sameOrigin(r) {
			fail(w, 403, "Cross-origin admin requests are not allowed")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, actor)))
	})
}

// sameOrigin requires the Origin header to match both the host and the scheme
// expected by this deployment. Behind a TLS proxy, r.TLS is nil and r.URL.Scheme
// is http, so the configured cookie policy determines the expected scheme.
func (s *server) sameOrigin(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || u.Scheme == "" || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if s.secure {
		return scheme == "https"
	}
	return scheme == "http"
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if !s.secure && !loopbackRequest(r) {
		fail(w, http.StatusForbidden, "Insecure administrator login requires a loopback connection and host. Enable secure cookies behind HTTPS.")
		return
	}
	if r.Header.Get("Origin") != "" && !s.sameOrigin(r) {
		fail(w, 403, "Invalid origin")
		return
	}
	if !s.loginLimiter.allow(clientIP(r)) {
		fail(w, http.StatusTooManyRequests, "Too many sign-in attempts. Try again in a minute.")
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if decode(w, r, &input) != nil {
		fail(w, 400, "Invalid JSON")
		return
	}
	actor, ok, err := s.authenticate(input.Token)
	if err != nil {
		databaseError(w, err)
		return
	}
	if !ok {
		s.metrics.authFailure()
		fail(w, 401, "That admin token is not valid.")
		return
	}
	// Store a revocable, hashed session instead of exposing the admin token in
	// a cookie. Legacy cookies containing the token remain valid for compatibility.
	session := analytics.ID("ses_")
	if err = s.store.CreateAdminSession(session, tokenHash(session), time.Now().UTC().Add(24*time.Hour)); err != nil {
		databaseError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "pulse_admin", Value: session, Path: "/", MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure})
	respond(w, 200, map[string]any{"ok": true, "user": actor})
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
	if !s.captureLimiter.allow(input.APIKey + "|" + clientIP(r)) {
		fail(w, http.StatusTooManyRequests, "Ingestion rate limit reached. Slow down or ask your operator to raise PULSE_CAPTURE_LIMIT_PER_MINUTE.")
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
	// A server-side batch may describe other people, so only infer location for
	// browser requests. IP addresses are never added to events or logs here.
	country := ""
	if origin != "" {
		country = s.country.country(r)
	}
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
		if country != "" {
			e.Properties["$country"] = country
		}
	}
	accepted, err := s.store.AddBatch(input.Events)
	if err != nil {
		databaseError(w, err)
		return
	}
	s.metrics.eventAccepted(int64(accepted))
	respond(w, 202, map[string]int{"accepted": accepted})
}
func (s *server) api(w http.ResponseWriter, r *http.Request) {
	if s.productAPI(w, r) {
		return
	}
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
			minimum := "owner"
			if r.Method == "PATCH" {
				minimum = "editor"
			}
			if !requireRole(w, r, minimum) {
				return
			}
			var v struct {
				ID       string   `json:"id"`
				Name     string   `json:"name"`
				Origins  []string `json:"origins"`
				Timezone string   `json:"timezone"`
			}
			if decode(w, r, &v) != nil {
				fail(w, 400, "Invalid project")
				return
			}
			if name := strings.TrimSpace(v.Name); name == "" || len(name) > 80 {
				fail(w, 400, "project name must contain 1–80 characters")
				return
			}
			if v.Timezone != "" {
				if err := analytics.ValidateTimezone(v.Timezone); err != nil {
					fail(w, 400, err.Error())
					return
				}
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
				if v.Timezone == "" {
					existing, ok, findErr := s.store.Project(v.ID)
					if findErr != nil {
						databaseError(w, findErr)
						return
					}
					if !ok {
						fail(w, 404, "Project not found")
						return
					}
					v.Timezone = existing.Timezone
				}
				p, err = s.store.UpdateProject(v.ID, v.Name, v.Origins, v.Timezone)
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
	if r.URL.Path == "/api/funnels/query" && r.Method == http.MethodPost {
		var input struct {
			Steps         []analytics.FunnelCondition `json:"steps"`
			WindowMinutes int                         `json:"window_minutes"`
			IncludePeople bool                        `json:"include_people"`
		}
		if decode(w, r, &input) != nil || len(input.Steps) < 2 || len(input.Steps) > 8 || input.WindowMinutes < 1 || input.WindowMinutes > 525600 {
			fail(w, 400, "Choose 2–8 steps and a valid conversion window")
			return
		}
		for _, step := range input.Steps {
			if strings.TrimSpace(step.Value) == "" || len(step.Value) > 500 || (step.Type != "page" && step.Type != "event") || (step.Operator != "eq" && !(step.Type == "page" && step.Operator == "starts_with")) {
				fail(w, 400, "Invalid funnel step")
				return
			}
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
		from, to, err := dateRange(r, projectLocation(p))
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		events, err := s.store.Events(p.ID, from, to)
		if err != nil {
			databaseError(w, err)
			return
		}
		events = analytics.FilterProperties(analytics.CanonicalizeIdentity(events), r.URL.Query().Get("property"), r.URL.Query().Get("value"))
		result := analytics.FunnelConditions(events, input.Steps, time.Duration(input.WindowMinutes)*time.Minute)
		if !input.IncludePeople {
			for i := range result {
				result[i].People = nil
				result[i].DropoffPeople = nil
			}
		}
		respond(w, 200, result)
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
	if r.URL.Path == "/api/funnels/suggestions" {
		kind, search := r.URL.Query().Get("type"), r.URL.Query().Get("q")
		if (kind != "page" && kind != "event") || len(search) > 500 {
			fail(w, 400, "Invalid suggestion query")
			return
		}
		values, err := s.store.FunnelSuggestions(p.ID, kind, search)
		if err != nil {
			databaseError(w, err)
			return
		}
		respond(w, 200, values)
		return
	}
	loc := projectLocation(p)
	from, to, err := dateRange(r, loc)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	events, err := s.store.Events(p.ID, from, to)
	if err != nil {
		databaseError(w, err)
		return
	}
	events = analytics.FilterProperties(analytics.CanonicalizeIdentity(events), r.URL.Query().Get("property"), r.URL.Query().Get("value"))
	switch r.URL.Path {
	case "/api/overview":
		previous, err := s.store.Events(p.ID, from.Add(-to.Sub(from)), from)
		if err != nil {
			databaseError(w, err)
			return
		}
		previous = analytics.FilterProperties(analytics.CanonicalizeIdentity(previous), r.URL.Query().Get("property"), r.URL.Query().Get("value"))
		recent := append([]analytics.Event{}, events...)
		sort.Slice(recent, func(i, j int) bool { return recent[i].Timestamp.After(recent[j].Timestamp) })
		if len(recent) > 8 {
			recent = recent[:8]
		}
		respond(w, 200, map[string]any{"metrics": analytics.Measure(events), "previous": analytics.Measure(previous), "decision": analytics.Decision(events, loc), "series": analytics.Series(events, from, to, loc), "top_events": analytics.Breakdown(events, ""), "pages": analytics.Breakdown(filterEvents(events, "$pageview"), "$pathname"), "browsers": analytics.Breakdown(events, "$browser"), "countries": analytics.Breakdown(events, "$country"), "recent": recent, "from": from, "to": to})
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
		page, err := positiveQueryInt(r, "page", 1, 1, 1_000_000)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		pageSize, err := positiveQueryInt(r, "page_size", 50, 10, 100)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		pages := (total + pageSize - 1) / pageSize
		if pages == 0 {
			pages = 1
		}
		if page > pages {
			page = pages
		}
		start := (page - 1) * pageSize
		end := start + pageSize
		if start > total {
			start = total
		}
		if end > total {
			end = total
		}
		respond(w, 200, map[string]any{"events": out[start:end], "total": total, "page": page, "page_size": pageSize, "pages": pages, "names": analytics.Breakdown(events, "")})
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
		// Load through "now" rather than to so cohorts keep maturing even when
		// the selected report window ends in the past.
		observeUntil := time.Now().UTC()
		all, err := s.store.RetentionEvents(p.ID, r.URL.Query().Get("property"), r.URL.Query().Get("value"))
		if err != nil {
			databaseError(w, err)
			return
		}
		all = analytics.FilterProperties(analytics.CanonicalizeIdentity(all), r.URL.Query().Get("property"), r.URL.Query().Get("value"))
		respond(w, 200, analytics.Retention(all, from, to, observeUntil, loc))
	case "/api/catalog":
		respond(w, 200, analytics.Catalog(events))
	case "/api/paths":
		respond(w, 200, analytics.Paths(events))
	case "/api/journey":
		if person := r.URL.Query().Get("person"); person != "" {
			filtered := []analytics.Event{}
			for _, event := range events {
				if event.DistinctID == person {
					filtered = append(filtered, event)
				}
			}
			events = filtered
		}
		maxDepth := analytics.JourneyMaxDepth(events)
		if maxDepth < 3 {
			maxDepth = 3
		}
		depth, err := positiveQueryInt(r, "steps", 3, 3, int(^uint(0)>>1))
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if depth > maxDepth {
			depth = maxDepth
		}
		selected := r.URL.Query()["selected"]
		if len(selected) > depth {
			fail(w, 400, "Too many selected steps")
			return
		}
		for _, item := range selected {
			if len(item) > 510 || !(strings.HasPrefix(item, "page:") || strings.HasPrefix(item, "event:")) {
				fail(w, 400, "Invalid journey step")
				return
			}
		}
		respond(w, 200, analytics.Journey(events, selected, depth))
	case "/api/export":
		w.Header().Set("Content-Disposition", "attachment; filename=pulse-events.json")
		respond(w, 200, events)
	default:
		fail(w, 404, "Endpoint not found")
	}
}

func positiveQueryInt(r *http.Request, name string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return value, nil
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

// projectLocation returns the project's configured timezone, falling back to
// UTC if a stored value is somehow invalid.
func projectLocation(p analytics.Project) *time.Location {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil || p.Timezone == "" {
		return time.UTC
	}
	return loc
}

func dateRange(r *http.Request, loc *time.Location) (time.Time, time.Time, error) {
	now := time.Now().In(loc)
	to := now
	from := analytics.DayStart(now, loc).AddDate(0, 0, -29)
	if v := r.URL.Query().Get("from"); v != "" {
		var err error
		from, err = time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			return from, to, fmt.Errorf("invalid start date")
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		d, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			return from, to, fmt.Errorf("invalid end date")
		}
		to = d.AddDate(0, 0, 1)
		if to.After(now) {
			to = now
		}
	}
	if !from.Before(to) {
		return from, to, errors.New("Choose a date range between 1 and 366 days")
	}
	// Count calendar days in the project's zone so a DST shift cannot turn a
	// valid 366-day range into an invalid one.
	days := 0
	last := analytics.DayStart(to, loc).AddDate(0, 0, 1)
	for d := analytics.DayStart(from, loc); d.Before(last); d = d.AddDate(0, 0, 1) {
		days++
		if days > 366 {
			return from, to, errors.New("Choose a date range between 1 and 366 days")
		}
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
