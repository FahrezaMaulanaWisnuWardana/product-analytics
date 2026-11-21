package analytics

import (
	"errors"
	"fmt"
	"pulse/internal/testdb"
	"testing"
	"time"
)

func TestProjectTimezoneValidationAndPersistence(t *testing.T) {
	s, err := Open(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.CreateProject("Zoned", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Timezone != "UTC" {
		t.Fatalf("new project timezone: %q", p.Timezone)
	}
	for _, invalid := range []string{"", "Local", "Mars/Olympus", "Asia/Jakarta/Extra"} {
		if err := ValidateTimezone(invalid); err == nil {
			t.Fatalf("invalid timezone accepted: %q", invalid)
		}
	}
	updated, err := s.UpdateProject(p.ID, "Zoned", nil, "Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Timezone != "Asia/Jakarta" {
		t.Fatalf("timezone not persisted: %q", updated.Timezone)
	}
	reloaded, ok, err := s.Project(p.ID)
	if err != nil || !ok || reloaded.Timezone != "Asia/Jakarta" {
		t.Fatalf("timezone not reloaded: %+v %v %v", reloaded, ok, err)
	}
	if _, err = s.UpdateProject(p.ID, "Zoned", nil, "Mars/Olympus"); err == nil {
		t.Fatal("invalid timezone reached the database")
	}
}

func TestWorkspaceProjectAndTokenLimits(t *testing.T) {
	s, err := Open(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < MaxProjects; i++ {
		if _, err = s.CreateProject(fmt.Sprintf("Project %d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.CreateProject("Over project limit", nil); !errors.Is(err, ErrWorkspaceLimit) {
		t.Fatal("project quota not enforced", err)
	}
	projects, err := s.Projects()
	if err != nil || len(projects) != MaxProjects {
		t.Fatalf("projects=%d err=%v", len(projects), err)
	}
	for i := 0; i < MaxAccessTokens; i++ {
		if _, err = s.CreateAccessToken(fmt.Sprintf("Token %d", i), "viewer", fmt.Sprintf("hash-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.CreateAccessToken("Over token limit", "viewer", "hash-over"); !errors.Is(err, ErrWorkspaceLimit) {
		t.Fatal("access-token quota not enforced", err)
	}
	tokens, err := s.AccessTokens()
	if err != nil || len(tokens) != MaxAccessTokens {
		t.Fatalf("tokens=%d err=%v", len(tokens), err)
	}
}

func TestWorkspaceHistoryRetention(t *testing.T) {
	s, err := Open(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token, err := s.CreateAccessToken("Old token", "viewer", "old-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeAccessToken(token.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Audit("owner", "test.old", "", token.ID, nil); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(-2, 0, 0)
	if _, err = s.db.Exec(`UPDATE access_tokens SET revoked_at=$1 WHERE id=$2`, old, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE audit_log SET created_at=$1 WHERE target_id=$2`, old, token.ID); err != nil {
		t.Fatal(err)
	}
	audits, tokens, err := s.PurgeWorkspaceHistory(time.Now().UTC().AddDate(-1, 0, 0), time.Now().UTC().AddDate(0, -3, 0))
	if err != nil {
		t.Fatal(err)
	}
	if audits != 1 || tokens != 1 {
		t.Fatalf("deleted audits=%d tokens=%d", audits, tokens)
	}
}

func TestProjectOriginLimits(t *testing.T) {
	origins := make([]string, MaxProjectOrigins+1)
	for i := range origins {
		origins[i] = fmt.Sprintf("https://%d.example.test", i)
	}
	if _, _, err := projectValues("Too many origins", origins); err == nil {
		t.Fatal("project origin cardinality limit not enforced")
	}
}
