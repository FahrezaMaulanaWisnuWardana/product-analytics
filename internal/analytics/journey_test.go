package analytics

import (
	"fmt"
	"testing"
	"time"
)

func TestJourneySelectionDoesNotReanchorCounts(t *testing.T) {
	var events []Event
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for person := 0; person < 29; person++ {
		steps := []string{"profiles", "details"}
		if person >= 21 {
			steps = []string{"home", "profiles", "details"}
		}
		// A later repeat must not become a new starting point when a selected
		// continuation doesn't match the person's original path.
		if person == 0 {
			steps = append(steps, "profiles", "export")
		}
		for i, name := range steps {
			events = append(events, Event{ID: fmt.Sprintf("%d-%d", person, i), Name: name, DistinctID: fmt.Sprint(person), Timestamp: base.Add(time.Duration(i) * time.Minute)})
		}
	}
	count := func(result JourneyResult, column int, id string) int {
		for _, node := range result.Columns[column].Nodes {
			if node.ID == id {
				return node.People
			}
		}
		return 0
	}
	unselected := Journey(events, nil, 3, 12)
	selected := Journey(events, []string{"event:profiles"}, 3, 12)
	continued := Journey(events, []string{"event:profiles", "event:details"}, 3, 12)
	for _, result := range []JourneyResult{unselected, selected, continued} {
		if got := count(result, 0, "event:profiles"); got != 21 {
			t.Fatalf("profiles count = %d, want 21", got)
		}
	}
	if got := count(selected, 1, "event:details"); got != 21 {
		t.Fatalf("details count = %d, want 21", got)
	}
	if got := count(continued, 1, "event:details"); got != 21 {
		t.Fatalf("selected details count = %d, want 21", got)
	}
	reanchored := Journey(events, []string{"event:profiles", "event:export"}, 3, 12)
	if reanchored.Columns[1].People != 0 {
		t.Fatalf("later repeated path was counted: %+v", reanchored.Columns[1])
	}
}
