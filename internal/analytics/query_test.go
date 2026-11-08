package analytics

import (
	"errors"
	"fmt"
	"pulse/internal/testdb"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEventQuotaAndQueryLimit(t *testing.T) {
	s, err := Open(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SetLimits(2, 1); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject("Limited", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	events := []Event{
		{ID: "one", ProjectID: p.ID, Name: "click", DistinctID: "u", Timestamp: now},
		{ID: "two", ProjectID: p.ID, Name: "click", DistinctID: "u", Timestamp: now},
	}
	if added, addErr := s.AddBatch(events); addErr != nil || added != 2 {
		t.Fatal("initial quota fill failed", added, addErr)
	}
	if added, addErr := s.AddBatch(events); addErr != nil || added != 0 {
		t.Fatal("idempotent retry should remain allowed at quota", added, addErr)
	}
	third := Event{ID: "three", ProjectID: p.ID, Name: "click", DistinctID: "u", Timestamp: now}
	if _, addErr := s.Add(third); !errors.Is(addErr, ErrEventQuota) {
		t.Fatal("event quota not enforced", addErr)
	}
	if _, queryErr := s.Events(p.ID, time.Time{}, now.Add(time.Hour)); !errors.Is(queryErr, ErrQueryEventLimit) {
		t.Fatal("query event limit not enforced", queryErr)
	}
}

func TestEventPropertiesByteLimit(t *testing.T) {
	s, err := Open(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.CreateProject("Property limit", nil)
	if err != nil {
		t.Fatal(err)
	}
	e := Event{ID: "large", ProjectID: p.ID, Name: "click", DistinctID: "u", Timestamp: time.Now().UTC(), Properties: map[string]any{"value": strings.Repeat("x", MaxEventPropertiesBytes)}}
	if _, err = s.Add(e); !errors.Is(err, ErrEventBytesLimit) {
		t.Fatal("oversized event properties accepted", err)
	}
}

func TestDeletePersonRemovesLinkedAnonymousEvents(t *testing.T) {
	s, err := Open(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.CreateProject("Deletion aliases", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	events := []Event{
		{ID: "anonymous", ProjectID: p.ID, Name: "viewed", DistinctID: "anon-1", Timestamp: now},
		{ID: "identify", ProjectID: p.ID, Name: "$identify", DistinctID: "user-1", Timestamp: now.Add(time.Second), Properties: map[string]any{"$anon_distinct_id": "anon-1"}},
		{ID: "identified", ProjectID: p.ID, Name: "clicked", DistinctID: "user-1", Timestamp: now.Add(2 * time.Second)},
	}
	if _, err = s.AddBatch(events); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeletePerson(p.ID, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 3 {
		t.Fatalf("deleted %d events; want 3", deleted)
	}
	if remaining := storedEvents(t, s, p.ID); len(remaining) != 0 {
		t.Fatalf("linked events remain: %+v", remaining)
	}
}

func at(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}
func TestFunnelOrderWindowAndUniquePeople(t *testing.T) {
	base := at("2026-10-01T00:00:00Z")
	events := []Event{
		{Name: "pay", DistinctID: "a", Timestamp: base}, // wrong order
		{Name: "visit", DistinctID: "a", Timestamp: base.Add(time.Hour)},
		{Name: "pay", DistinctID: "a", Timestamp: base.Add(2 * time.Hour)},
		{Name: "pay", DistinctID: "a", Timestamp: base.Add(3 * time.Hour)}, // count person only once
		{Name: "visit", DistinctID: "b", Timestamp: base},
		{Name: "pay", DistinctID: "b", Timestamp: base.Add(8 * 24 * time.Hour)}, // outside window
		{Name: "pay", DistinctID: "c", Timestamp: base},
	}
	f := Funnel(events, []string{"visit", "pay"}, 7*24*time.Hour)
	if f[0].Users != 2 || f[1].Users != 1 || f[1].Rate != 50 || f[1].Dropoff != 1 {
		t.Fatalf("unexpected funnel: %+v", f)
	}
	if len(f[1].People) != 1 || f[1].People[0] != "a" || len(f[1].DropoffPeople) != 1 || f[1].DropoffPeople[0] != "b" {
		t.Fatalf("unexpected funnel drill-down: %+v", f[1])
	}
}

func TestIdentitySegmentsCatalogAndPaths(t *testing.T) {
	base := at("2026-10-01T00:00:00Z")
	events := []Event{
		{Name: "$pageview", DistinctID: "p_anon", SessionID: "s1", Timestamp: base, Properties: map[string]any{"plan": "Pro", "score": 1.0}},
		{Name: "$identify", DistinctID: "user-1", SessionID: "s1", Timestamp: base.Add(time.Minute), Properties: map[string]any{"$anon_distinct_id": "p_anon", "plan": "Pro"}},
		{Name: "created", DistinctID: "user-1", SessionID: "s1", Timestamp: base.Add(2 * time.Minute), Properties: map[string]any{"plan": "Pro", "score": "high"}},
		{Name: "created", DistinctID: "user-2", Timestamp: base.Add(3 * time.Minute), Properties: map[string]any{"plan": "Free", "score": 2.0}},
	}
	canonical := CanonicalizeIdentity(events)
	if got := Measure(canonical).Users; got != 2 {
		t.Fatalf("identity was not merged: got %d users", got)
	}
	pro := FilterProperties(canonical, "plan", "Pro")
	if len(pro) != 3 {
		t.Fatalf("segment returned %d events", len(pro))
	}
	paths := Paths(pro)
	if len(paths) != 2 || paths[0].Users != 1 {
		t.Fatalf("unexpected paths: %+v", paths)
	}
	health := Catalog(canonical)
	if health.Events != 4 || health.EventTypes != 3 || health.SessionRate != 75 {
		t.Fatalf("unexpected health summary: %+v", health)
	}
	var created EventDefinition
	for _, definition := range health.Definitions {
		if definition.Name == "created" {
			created = definition
		}
	}
	if len(created.Warnings) != 1 || created.Warnings[0] != "score changes type" {
		t.Fatalf("schema drift was not detected: %+v", created)
	}
}
func TestRetentionUsesFirstEverEventAndUnobservedDays(t *testing.T) {
	start := at("2026-10-01T00:00:00Z")
	events := []Event{
		{DistinctID: "old", Timestamp: start.Add(-24 * time.Hour)},
		{DistinctID: "old", Timestamp: start},
		{DistinctID: "new", Timestamp: start},
		{DistinctID: "new", Timestamp: start.Add(time.Hour)},
		{DistinctID: "new", Timestamp: start.Add(24 * time.Hour)},
		{DistinctID: "other", Timestamp: start},
	}
	r := Retention(events, start, start.Add(48*time.Hour))
	if r[0].Size != 2 || *r[0].Retained[0] != 100 || *r[0].Retained[1] != 50 || r[0].Retained[2] != nil {
		t.Fatalf("unexpected retention: %+v", r[0])
	}
}
func TestMeasureAndSeries(t *testing.T) {
	start := at("2026-10-01T00:00:00Z")
	events := []Event{{Name: "$pageview", DistinctID: "a", SessionID: "same", Timestamp: start}, {Name: "click", DistinctID: "a", SessionID: "same", Timestamp: start}, {Name: "click", DistinctID: "b", SessionID: "same", Timestamp: start}}
	m := Measure(events)
	if m.Users != 2 || m.Events != 3 || m.Sessions != 2 || m.Pageviews != 1 {
		t.Fatalf("metrics: %+v", m)
	}
	series := Series(events, start, start.Add(48*time.Hour))
	if len(series) != 2 || series[0].Users != 2 || series[1].Events != 0 || series[0].Pageviews != 1 || series[0].Sessions != 2 {
		t.Fatalf("series: %+v", series)
	}
}

func TestDecisionContextReadinessAndConcentration(t *testing.T) {
	start := at("2026-10-01T00:00:00Z")
	events := []Event{
		{Name: "detail_viewed", DistinctID: "a", Timestamp: start},
		{Name: "detail_viewed", DistinctID: "a", Timestamp: start.Add(time.Minute)},
		{Name: "detail_viewed", DistinctID: "b", Timestamp: start.Add(2 * time.Minute)},
		{Name: "signed_up", DistinctID: "c", Timestamp: start.Add(3 * time.Minute)},
	}
	d := Decision(events)
	if d.Readiness != "collecting" || d.ActiveDays != 1 {
		t.Fatalf("readiness: %+v", d)
	}
	if d.TopEvent.Name != "detail_viewed" || d.TopEvent.Count != 3 || d.TopEvent.Users != 2 || d.TopEventShare != 75 {
		t.Fatalf("top event: %+v", d)
	}
	if d.TopPersonEvents != 2 || d.TopPersonShare != 50 {
		t.Fatalf("person concentration: %+v", d)
	}
}

func TestDecisionContextReadySample(t *testing.T) {
	start := at("2026-10-01T00:00:00Z")
	events := make([]Event, 0, 30)
	for i := 0; i < 30; i++ {
		events = append(events, Event{Name: "active", DistinctID: fmt.Sprintf("user-%d", i), Timestamp: start.AddDate(0, 0, i%7)})
	}
	if d := Decision(events); d.Readiness != "ready" || d.ActiveDays != 7 {
		t.Fatalf("expected ready sample: %+v", d)
	}
}
func TestStorePersistenceIsolationAndProjectUpdate(t *testing.T) {
	dir := testdb.URL(t)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject("First", []string{"https://one.example"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateProject("Second", nil)
	if err != nil {
		t.Fatal(err)
	}
	e := Event{ID: "test", Name: "visit", ProjectID: p.ID, DistinctID: "u", Timestamp: time.Now()}
	if added, err := s.Add(e); !added || err != nil {
		t.Fatal("first event not accepted", err)
	}
	if added, err := s.Add(e); added || err != nil {
		t.Fatal("duplicate accepted", err)
	}
	if _, err = s.UpdateProject(p.ID, "Renamed", []string{"https://two.example"}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	projects, err := s.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatal("update created an extra project")
	}
	restored, ok, err := s.Project(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || restored.Name != "Renamed" || restored.Key != p.Key || restored.Origins[0] != "https://two.example" {
		t.Fatalf("project did not persist: %+v", restored)
	}
	if len(storedEvents(t, s, p.ID)) != 1 {
		t.Fatal("event did not persist")
	}
	if len(storedEvents(t, s, other.ID)) != 0 {
		t.Fatal("events leaked between projects")
	}
	if added, _ := s.Add(e); added {
		t.Fatal("deduplication did not survive restart")
	}
}

func TestConcurrentBatchesAndRejectedEncoding(t *testing.T) {
	s, err := Open(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.CreateProject("Concurrent", nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := Event{ID: fmt.Sprint(i), ProjectID: p.ID, Name: "click", DistinctID: "u", Timestamp: time.Now()}
			if _, err := s.AddBatch([]Event{e, e}); err != nil {
				t.Error(err)
			}
			if _, err := s.Events(p.ID, time.Time{}, time.Now().Add(time.Hour)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if got := len(storedEvents(t, s, p.ID)); got != 20 {
		t.Fatalf("got %d events; want 20", got)
	}
	_, err = s.AddBatch([]Event{{ID: "valid", ProjectID: p.ID, Name: "click", DistinctID: "u", Timestamp: time.Now()}, {ID: "invalid", Properties: map[string]any{"unsupported": func() {}}}})
	if err == nil {
		t.Fatal("unsupported JSON value accepted")
	}
	if got := len(storedEvents(t, s, p.ID)); got != 20 {
		t.Fatal("failed batch was partially persisted")
	}
}

func storedEvents(t *testing.T, s *Store, project string) []Event {
	t.Helper()
	events, err := s.Events(project, time.Time{}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return events
}
