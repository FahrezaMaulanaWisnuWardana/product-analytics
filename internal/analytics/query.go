package analytics

import (
	"sort"
	"time"
)

type Point struct {
	Date      string `json:"date"`
	Users     int    `json:"users"`
	Events    int    `json:"events"`
	Sessions  int    `json:"sessions"`
	Pageviews int    `json:"pageviews"`
}
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Users int    `json:"users"`
}
type Metrics struct {
	Users     int `json:"users"`
	Events    int `json:"events"`
	Sessions  int `json:"sessions"`
	Pageviews int `json:"pageviews"`
}

// DecisionContext describes whether the selected sample is broad enough for
// product decisions and where activity is concentrated. Readiness is a
// deliberately conservative heuristic, not a statistical significance test.
type DecisionContext struct {
	Readiness       string  `json:"readiness"`
	ActiveDays      int     `json:"active_days"`
	FirstEvent      string  `json:"first_event,omitempty"`
	LastEvent       string  `json:"last_event,omitempty"`
	TopEvent        Count   `json:"top_event"`
	TopEventShare   float64 `json:"top_event_share"`
	TopPersonEvents int     `json:"top_person_events"`
	TopPersonShare  float64 `json:"top_person_share"`
}

func Measure(events []Event) Metrics {
	users := map[string]bool{}
	sessions := map[string]bool{}
	m := Metrics{Events: len(events)}
	for _, e := range events {
		users[e.DistinctID] = true
		if e.SessionID != "" {
			sessions[e.DistinctID+"\x00"+e.SessionID] = true
		}
		if e.Name == "$pageview" {
			m.Pageviews++
		}
	}
	m.Users = len(users)
	m.Sessions = len(sessions)
	return m
}

func Decision(events []Event) DecisionContext {
	result := DecisionContext{Readiness: "collecting"}
	if len(events) == 0 {
		return result
	}

	days := map[string]bool{}
	people := map[string]int{}
	first, last := events[0].Timestamp, events[0].Timestamp
	for _, e := range events {
		days[e.Timestamp.UTC().Format("2006-01-02")] = true
		people[e.DistinctID]++
		if e.Timestamp.Before(first) {
			first = e.Timestamp
		}
		if e.Timestamp.After(last) {
			last = e.Timestamp
		}
	}

	result.ActiveDays = len(days)
	result.FirstEvent = first.UTC().Format(time.RFC3339)
	result.LastEvent = last.UTC().Format(time.RFC3339)
	if breakdown := Breakdown(events, ""); len(breakdown) > 0 {
		result.TopEvent = breakdown[0]
		result.TopEventShare = float64(breakdown[0].Count) * 100 / float64(len(events))
	}
	for _, count := range people {
		if count > result.TopPersonEvents {
			result.TopPersonEvents = count
		}
	}
	result.TopPersonShare = float64(result.TopPersonEvents) * 100 / float64(len(events))

	users := len(people)
	switch {
	case users >= 30 && result.ActiveDays >= 7:
		result.Readiness = "ready"
	case users >= 10 && result.ActiveDays >= 3:
		result.Readiness = "directional"
	}
	return result
}
func Series(events []Event, from, to time.Time) []Point {
	counts := map[string]int{}
	users := map[string]map[string]bool{}
	sessions := map[string]map[string]bool{}
	pageviews := map[string]int{}
	for _, e := range events {
		k := e.Timestamp.UTC().Format("2006-01-02")
		counts[k]++
		if users[k] == nil {
			users[k] = map[string]bool{}
		}
		users[k][e.DistinctID] = true
		if sessions[k] == nil {
			sessions[k] = map[string]bool{}
		}
		if e.SessionID != "" {
			sessions[k][e.DistinctID+"\x00"+e.SessionID] = true
		}
		if e.Name == "$pageview" {
			pageviews[k]++
		}
	}
	out := []Point{}
	for d := from.UTC().Truncate(24 * time.Hour); d.Before(to); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		out = append(out, Point{k, len(users[k]), counts[k], len(sessions[k]), pageviews[k]})
	}
	return out
}
func Breakdown(events []Event, property string) []Count {
	counts := map[string]int{}
	users := map[string]map[string]bool{}
	for _, e := range events {
		key := e.Name
		if property != "" {
			key, _ = e.Properties[property].(string)
			if key == "" {
				key = "Unknown"
			}
		}
		counts[key]++
		if users[key] == nil {
			users[key] = map[string]bool{}
		}
		users[key][e.DistinctID] = true
	}
	out := []Count{}
	for k, n := range counts {
		out = append(out, Count{k, n, len(users[k])})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Name < out[j].Name
		}
		return out[i].Count > out[j].Count
	})
	return out
}

type FunnelStep struct {
	Name    string  `json:"name"`
	Users   int     `json:"users"`
	Rate    float64 `json:"rate"`
	Dropoff int     `json:"dropoff"`
}

// Funnel counts people completing steps in order within a conversion window.
func Funnel(events []Event, steps []string, window time.Duration) []FunnelStep {
	sorted := append([]Event{}, events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp.Before(sorted[j].Timestamp) })
	type progress struct {
		next  int
		start time.Time
	}
	states := map[string]progress{}
	reached := make([]map[string]bool, len(steps))
	for i := range reached {
		reached[i] = map[string]bool{}
	}
	for _, e := range sorted {
		p := states[e.DistinctID]
		if p.next > 0 && e.Timestamp.Sub(p.start) > window {
			p = progress{}
		}
		if p.next < len(steps) && e.Name == steps[p.next] {
			if p.next == 0 {
				p.start = e.Timestamp
			}
			reached[p.next][e.DistinctID] = true
			p.next++
		}
		states[e.DistinctID] = p
	}
	out := []FunnelStep{}
	base := 0
	if len(steps) > 0 {
		base = len(reached[0])
	}
	for i, n := range steps {
		v := len(reached[i])
		rate := 0.0
		if base > 0 {
			rate = float64(v) / float64(base) * 100
		}
		drop := 0
		if i > 0 {
			drop = len(reached[i-1]) - v
		}
		out = append(out, FunnelStep{n, v, rate, drop})
	}
	return out
}

type Person struct {
	ID         string         `json:"id"`
	Events     int            `json:"events"`
	LastSeen   time.Time      `json:"last_seen"`
	Properties map[string]any `json:"properties"`
}

func People(events []Event) []Person {
	people := map[string]Person{}
	for _, e := range events {
		p := people[e.DistinctID]
		p.ID = e.DistinctID
		p.Events++
		if e.Timestamp.After(p.LastSeen) {
			p.LastSeen = e.Timestamp
			p.Properties = e.Properties
		}
		people[e.DistinctID] = p
	}
	out := []Person{}
	for _, p := range people {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

type Cohort struct {
	Date     string     `json:"date"`
	Size     int        `json:"size"`
	Retained []*float64 `json:"retained"`
}

// Daily retention uses the person's first observed event in the project's complete history.
func Retention(all []Event, from, to time.Time) []Cohort {
	first := map[string]time.Time{}
	active := map[string]map[string]bool{}
	for _, e := range all {
		d := e.Timestamp.UTC().Truncate(24 * time.Hour)
		if prev, ok := first[e.DistinctID]; !ok || d.Before(prev) {
			first[e.DistinctID] = d
		}
		if active[e.DistinctID] == nil {
			active[e.DistinctID] = map[string]bool{}
		}
		active[e.DistinctID][d.Format("2006-01-02")] = true
	}
	out := []Cohort{}
	for day := from.UTC().Truncate(24 * time.Hour); day.Before(to); day = day.AddDate(0, 0, 1) {
		members := []string{}
		for id, d := range first {
			if d.Equal(day) {
				members = append(members, id)
			}
		}
		c := Cohort{Date: day.Format("2006-01-02"), Size: len(members), Retained: []*float64{}}
		for n := 0; n < 8; n++ {
			d := day.AddDate(0, 0, n)
			var pct *float64
			if d.Before(to) && len(members) > 0 {
				count := 0
				for _, id := range members {
					if active[id][d.Format("2006-01-02")] {
						count++
					}
				}
				v := float64(count) * 100 / float64(len(members))
				pct = &v
			}
			c.Retained = append(c.Retained, pct)
		}
		out = append(out, c)
	}
	return out
}
