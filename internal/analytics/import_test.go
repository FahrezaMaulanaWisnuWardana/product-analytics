package analytics

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"pulse/internal/testdb"
)

func legacy(projects []Project, events []Event) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, p := range projects {
		_ = enc.Encode(map[string]any{"project": p})
	}
	for _, e := range events {
		_ = enc.Encode(map[string]any{"event": e})
	}
	return b.String()
}
func TestLegacyImportPreservesIDsAndIsRepeatable(t *testing.T) {
	s, err := Open(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := Project{ID: "legacy-project", Name: "Before", Key: "pk_legacy", Origins: []string{"https://one.example"}, CreatedAt: time.Now().UTC()}
	update := p
	update.Name = "Latest"
	update.Origins = []string{"https://two.example"}
	event := Event{ID: "legacy-event", ProjectID: p.ID, Name: "paid", DistinctID: "u1", Timestamp: time.Now().UTC(), Properties: map[string]any{"amount": 29.0}}
	source := legacy([]Project{p, update}, []Event{event, event})
	result, err := s.ImportJSONL(strings.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	if result.Projects != 1 || result.Events != 1 {
		t.Fatalf("unexpected import: %+v", result)
	}
	got, ok, err := s.Project(p.ID)
	if err != nil || !ok || got.Key != p.Key || got.Name != "Latest" || got.Origins[0] != "https://two.example" {
		t.Fatalf("project changed: %+v %v", got, err)
	}
	events := storedEvents(t, s, p.ID)
	if len(events) != 1 || events[0].ID != event.ID || events[0].Properties["amount"] != 29.0 {
		t.Fatal("event changed", events)
	}
	if _, err = s.UpdateProject(p.ID, "Edited after import", nil); err != nil {
		t.Fatal(err)
	}
	result, err = s.ImportJSONL(strings.NewReader(source))
	if err != nil || result.Projects != 0 || result.Events != 0 {
		t.Fatal(result, err)
	}
	got, _, _ = s.Project(p.ID)
	if got.Name != "Edited after import" {
		t.Fatal("repeat import overwrote an existing project")
	}
}
func TestImportAndBatchRollback(t *testing.T) {
	s, err := Open(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := Project{ID: "rollback", Name: "Rollback", Key: "pk_rollback", CreatedAt: time.Now().UTC()}
	source := legacy([]Project{p}, []Event{{ID: "bad", ProjectID: "missing-project", Name: "click", DistinctID: "u", Timestamp: time.Now()}})
	if _, err = s.ImportJSONL(strings.NewReader(source)); err == nil {
		t.Fatal("orphan event accepted")
	}
	if _, ok, _ := s.Project(p.ID); ok {
		t.Fatal("failed import left partial project")
	}
	if _, err = s.ImportJSONL(strings.NewReader(legacy([]Project{p}, nil) + "{broken")); err == nil {
		t.Fatal("corrupt record accepted")
	}
	if _, ok, _ := s.Project(p.ID); ok {
		t.Fatal("invalid file left partial project")
	}
	real, err := s.CreateProject("Real", nil)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{{ID: "one", ProjectID: real.ID, Name: "click", DistinctID: "u", Timestamp: time.Now()}, {ID: "two", ProjectID: "missing", Name: "click", DistinctID: "u", Timestamp: time.Now()}}
	if _, err = s.AddBatch(events); err == nil {
		t.Fatal("foreign key violation accepted")
	}
	if len(storedEvents(t, s, real.ID)) != 0 {
		t.Fatal("batch partially persisted")
	}
	conflict := real
	conflict.Key = "different-key"
	if _, err = s.ImportJSONL(strings.NewReader(legacy([]Project{conflict}, nil))); err == nil {
		t.Fatal("conflicting ingestion key accepted")
	}
}
func TestConcurrentDuplicateBatchesAcrossConnections(t *testing.T) {
	dsn := testdb.URL(t)
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	p, err := s.CreateProject("Shared", nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := []Event{}
	for i := 0; i < 10; i++ {
		batch = append(batch, Event{ID: fmt.Sprint(i), ProjectID: p.ID, Name: "click", DistinctID: "u", Timestamp: time.Now()})
	}
	reversed := append([]Event{}, batch...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 2)
	go func() { n, err := s.AddBatch(batch); done <- result{n, err} }()
	go func() { n, err := other.AddBatch(reversed); done <- result{n, err} }()
	a, b := <-done, <-done
	if a.err != nil || b.err != nil || a.n+b.n != 10 {
		t.Fatal(a, b)
	}
	if len(storedEvents(t, s, p.ID)) != 10 {
		t.Fatal("duplicates persisted")
	}
}
