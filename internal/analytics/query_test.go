package analytics

import (
	"errors"
	"fmt"
	"pulse/internal/testdb"
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
